package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/example/agent-platform/backend/internal/database/model"
	"github.com/example/agent-platform/backend/internal/domain"
	"github.com/example/agent-platform/backend/internal/knowledgelex"
	pgvector "github.com/pgvector/pgvector-go"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestPostgresKnowledgeOwnershipVectorAndCitationSnapshot(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	repositories := New(db)
	suffix := time.Now().UnixNano()
	owner := model.User{Email: fmt.Sprintf("knowledge-owner-%d@example.com", suffix), PasswordHash: "test-only", DisplayName: "Knowledge Owner"}
	other := model.User{Email: fmt.Sprintf("knowledge-other-%d@example.com", suffix), PasswordHash: "test-only", DisplayName: "Knowledge Other"}
	for _, user := range []*model.User{&owner, &other} {
		if err := repositories.Users.Create(ctx, user); err != nil {
			t.Fatal(err)
		}
		defer db.WithContext(ctx).Delete(user)
	}
	base := model.KnowledgeBase{Name: "产品知识库", Description: "集成测试"}
	if err := repositories.Knowledge.CreateBase(ctx, owner.ID, &base); err != nil {
		t.Fatal(err)
	}
	if base.UserID != owner.ID {
		t.Fatalf("knowledge base user ID = %q, want %q", base.UserID, owner.ID)
	}
	document := model.KnowledgeDocument{
		KnowledgeBaseID: base.ID, ObjectKey: fmt.Sprintf("knowledge/integration-test-%d.txt", suffix), OriginalName: "规则.txt",
		Extension: ".txt", MIMEType: "text/plain", Size: 10, Checksum: fmt.Sprintf("%064x", suffix),
	}
	unauthorizedDocument := document
	unauthorizedDocument.ObjectKey += ".unauthorized"
	if err := repositories.Knowledge.CreateDocument(ctx, other.ID, &unauthorizedDocument); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-user CreateDocument() error = %v, want ErrNotFound", err)
	}
	if err := repositories.Knowledge.CreateDocument(ctx, owner.ID, &document); err != nil {
		t.Fatal(err)
	}
	if _, err := repositories.Knowledge.BeginDocumentIngestion(ctx, other.ID, base.ID, document.ID, []string{model.KnowledgeDocumentStatusUploaded}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-user BeginDocumentIngestion() error = %v, want ErrNotFound", err)
	}
	ingesting, err := repositories.Knowledge.BeginDocumentIngestion(ctx, owner.ID, base.ID, document.ID, []string{model.KnowledgeDocumentStatusUploaded})
	if err != nil || ingesting.IngestionVersion != 1 || ingesting.Status != model.KnowledgeDocumentStatusExtracting {
		t.Fatalf("BeginDocumentIngestion() = %+v, %v", ingesting, err)
	}
	if err := repositories.Knowledge.AdvanceDocumentIngestion(ctx, owner.ID, document.ID, ingesting.IngestionVersion, model.KnowledgeDocumentStatusExtracting, model.KnowledgeDocumentStatusChunking, "提取后的正文"); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Knowledge.AdvanceDocumentIngestion(ctx, owner.ID, document.ID, ingesting.IngestionVersion, model.KnowledgeDocumentStatusChunking, model.KnowledgeDocumentStatusEmbedding, ""); err != nil {
		t.Fatal(err)
	}

	firstEmbedding := pgvector.NewVector([]float32{1, 0, 0})
	secondEmbedding := pgvector.NewVector([]float32{0, 1, 0})
	chunks := []model.KnowledgeChunk{
		{ChunkIndex: 0, Content: "退款可以在七日内申请", ContentHash: fmt.Sprintf("%064d", 1), TokenCount: 10, Embedding: &firstEmbedding, EmbeddingModel: "test-embedding", EmbeddingDimension: 3},
		{ChunkIndex: 1, Content: "企业账户支持多人协作", ContentHash: fmt.Sprintf("%064d", 2), TokenCount: 12, Embedding: &secondEmbedding, EmbeddingModel: "test-embedding", EmbeddingDimension: 3},
	}
	if err := repositories.Knowledge.CompleteDocumentIngestion(ctx, owner.ID, document.ID, ingesting.IngestionVersion, chunks, "test", "test-embedding", 3, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Knowledge.ReplaceChunks(ctx, other.ID, document.ID, chunks); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-user ReplaceChunks() error = %v, want ErrNotFound", err)
	}
	stored, err := repositories.Knowledge.ListChunks(ctx, owner.ID, document.ID)
	if err != nil || len(stored) != 2 || stored[0].Embedding == nil {
		t.Fatalf("ListChunks() = %#v, error = %v", stored, err)
	}
	foreign, err := repositories.Knowledge.ListChunks(ctx, other.ID, document.ID)
	if err != nil || len(foreign) != 0 {
		t.Fatalf("cross-user ListChunks() = %#v, error = %v", foreign, err)
	}
	keywordQuery := pgvector.NewVector([]float32{0, 1, 0})
	keywordRows, err := repositories.Knowledge.SearchChunksKeyword(ctx, owner.ID, []string{base.ID}, "test", "test-embedding", 3, 10, "退款", keywordQuery)
	if err != nil || len(keywordRows) != 1 || keywordRows[0].ChunkIndex != 0 || keywordRows[0].KeywordScore <= 0 {
		t.Fatalf("中文关键词独立召回失败: %+v, %v", keywordRows, err)
	}
	foreignKeyword, err := repositories.Knowledge.SearchChunksKeyword(ctx, other.ID, []string{base.ID}, "test", "test-embedding", 3, 10, "退款", keywordQuery)
	if err != nil || len(foreignKeyword) != 0 {
		t.Fatalf("关键词查询越权: %+v, %v", foreignKeyword, err)
	}
	wrongModelKeyword, err := repositories.Knowledge.SearchChunksKeyword(ctx, owner.ID, []string{base.ID}, "test", "other-model", 3, 10, "退款", keywordQuery)
	if err != nil || len(wrongModelKeyword) != 0 {
		t.Fatalf("关键词查询未过滤模型版本: %+v, %v", wrongModelKeyword, err)
	}
	if err := db.Model(&model.KnowledgeChunk{}).Where("id = ?", stored[0].ID).
		UpdateColumns(map[string]any{"keyword_text": "", "keyword_version": 0}).Error; err != nil {
		t.Fatal(err)
	}
	if err := migrateKnowledgeKeywordIndex(ctx, db); err != nil {
		t.Fatal(err)
	}
	var backfilled model.KnowledgeChunk
	if err := db.First(&backfilled, "id = ?", stored[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if backfilled.KeywordVersion != knowledgelex.Version || backfilled.KeywordText == "" {
		t.Fatal("旧片段关键词回填失败")
	}
	equalQuery := pgvector.NewVector([]float32{1, 1, 0})
	equalRows, err := repositories.Knowledge.SearchChunksExact(ctx, owner.ID, []string{base.ID}, "test", "test-embedding", 3, 10, equalQuery)
	if err != nil {
		t.Fatal(err)
	}
	if len(equalRows) != 2 || equalRows[0].ChunkIndex != 0 || equalRows[1].ChunkIndex != 1 {
		t.Fatalf("same-score search is not stable: %+v", equalRows)
	}
	crossUserRows, err := repositories.Knowledge.SearchChunksExact(ctx, other.ID, []string{base.ID}, "test", "test-embedding", 3, 10, equalQuery)
	if err != nil || len(crossUserRows) != 0 {
		t.Fatalf("cross-user search = %+v, error = %v", crossUserRows, err)
	}
	differentDimension := pgvector.NewVector([]float32{1, 1, 0, 0})
	dimensionRows, err := repositories.Knowledge.SearchChunksExact(ctx, owner.ID, []string{base.ID}, "test", "test-embedding", 4, 10, differentDimension)
	if err != nil || len(dimensionRows) != 0 {
		t.Fatalf("dimension-mismatched search = %+v, error = %v", dimensionRows, err)
	}
	modelRows, err := repositories.Knowledge.SearchChunksExact(ctx, owner.ID, []string{base.ID}, "test", "another-embedding", 3, 10, equalQuery)
	if err != nil || len(modelRows) != 0 {
		t.Fatalf("model-mismatched search = %+v, error = %v", modelRows, err)
	}
	if err := db.WithContext(ctx).Model(&model.KnowledgeDocument{}).Where("id = ?", document.ID).Update("status", model.KnowledgeDocumentStatusFailed).Error; err != nil {
		t.Fatal(err)
	}
	notReadyRows, err := repositories.Knowledge.SearchChunksExact(ctx, owner.ID, []string{base.ID}, "test", "test-embedding", 3, 10, equalQuery)
	if err != nil || len(notReadyRows) != 0 {
		t.Fatalf("not-ready document leaked into search: %+v, error = %v", notReadyRows, err)
	}
	notReadyKeyword, err := repositories.Knowledge.SearchChunksKeyword(ctx, owner.ID, []string{base.ID}, "test", "test-embedding", 3, 10, "退款", keywordQuery)
	if err != nil || len(notReadyKeyword) != 0 {
		t.Fatalf("未就绪文档进入关键词结果: %+v, %v", notReadyKeyword, err)
	}
	if err := db.WithContext(ctx).Model(&model.KnowledgeDocument{}).Where("id = ?", document.ID).Update("status", model.KnowledgeDocumentStatusReady).Error; err != nil {
		t.Fatal(err)
	}

	queryVector := pgvector.NewVector([]float32{0.9, 0.1, 0})
	nearestRows, err := repositories.Knowledge.SearchChunksExact(ctx, owner.ID, []string{base.ID}, "test", "test-embedding", 3, 1, queryVector)
	if err != nil {
		t.Fatal(err)
	}
	if len(nearestRows) != 1 || nearestRows[0].ChunkIndex != 0 {
		t.Fatalf("nearest chunks = %+v, want chunk index 0", nearestRows)
	}

	conversation := model.Conversation{UserID: owner.ID, Title: "引用测试", TitleStatus: "manual"}
	if err := repositories.Conversations.Create(ctx, &conversation); err != nil {
		t.Fatal(err)
	}
	message := model.Message{ConversationID: conversation.ID, Role: "assistant", Status: "streaming"}
	if err := db.WithContext(ctx).Create(&message).Error; err != nil {
		t.Fatal(err)
	}
	completed, err := repositories.Messages.Finalize(ctx, message.ID, model.MessageCompletion{
		Content: "退款期限为七日。[1]", Status: "complete", FinishReason: "stop",
		Citations: []model.MessageCitation{{
			KnowledgeBaseID: &base.ID, DocumentID: &document.ID, ChunkID: &stored[0].ID,
			Ordinal: 1, SourceName: document.OriginalName, Quote: stored[0].Content, SimilarityScore: 0.95,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(completed.Citations) != 1 || completed.Citations[0].MessageID != message.ID {
		t.Fatalf("finalized citations = %+v", completed.Citations)
	}
	citation := completed.Citations[0]

	rollbackMessage := model.Message{ConversationID: conversation.ID, Role: "assistant", Status: "streaming"}
	if err := db.WithContext(ctx).Create(&rollbackMessage).Error; err != nil {
		t.Fatal(err)
	}
	_, err = repositories.Messages.Finalize(ctx, rollbackMessage.ID, model.MessageCompletion{
		Content: "不应提交", Status: "complete",
		Citations: []model.MessageCitation{
			{Ordinal: 1, SourceName: "一", Quote: "一"},
			{Ordinal: 1, SourceName: "二", Quote: "二"},
		},
	})
	if err == nil {
		t.Fatal("expected duplicate citation ordinal to roll back finalization")
	}
	var rolledBack model.Message
	if err := db.WithContext(ctx).First(&rolledBack, "id = ?", rollbackMessage.ID).Error; err != nil {
		t.Fatal(err)
	}
	if rolledBack.Status != "streaming" || rolledBack.Content != "" {
		t.Fatalf("message finalization was not rolled back: %+v", rolledBack)
	}
	var rolledBackCitationCount int64
	if err := db.WithContext(ctx).Model(&model.MessageCitation{}).Where("message_id = ?", rollbackMessage.ID).Count(&rolledBackCitationCount).Error; err != nil {
		t.Fatal(err)
	}
	if rolledBackCitationCount != 0 {
		t.Fatalf("rolled-back citations = %d", rolledBackCitationCount)
	}
	if err := db.WithContext(ctx).Delete(&rollbackMessage).Error; err != nil {
		t.Fatal(err)
	}
	deletedDuringGeneration := model.Message{ConversationID: conversation.ID, Role: "assistant", Status: "streaming"}
	if err := db.WithContext(ctx).Create(&deletedDuringGeneration).Error; err != nil {
		t.Fatal(err)
	}
	if err := repositories.Knowledge.DeleteBaseWithCleanup(ctx, owner.ID, base.ID); err != nil {
		t.Fatal(err)
	}
	cleanupTasks, err := repositories.Knowledge.ListPendingObjectCleanup(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	foundCleanup := false
	for _, task := range cleanupTasks {
		if task.ObjectKey == document.ObjectKey {
			foundCleanup = true
			if err := repositories.Knowledge.CompleteObjectCleanup(ctx, task.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !foundCleanup {
		t.Fatal("knowledge object cleanup task was not created transactionally")
	}
	completedAfterDelete, err := repositories.Messages.Finalize(ctx, deletedDuringGeneration.ID, model.MessageCompletion{
		Content: "删除期间仍保留快照[1]", Status: "complete", FinishReason: "stop",
		Citations: []model.MessageCitation{{
			KnowledgeBaseID: &base.ID, DocumentID: &document.ID, ChunkID: &stored[0].ID,
			Ordinal: 1, SourceName: document.OriginalName, Quote: stored[0].Content, SimilarityScore: 0.95,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(completedAfterDelete.Citations) != 1 || completedAfterDelete.Citations[0].KnowledgeBaseID != nil || completedAfterDelete.Citations[0].Quote == "" {
		t.Fatalf("citation finalized after source deletion = %+v", completedAfterDelete.Citations)
	}
	var preserved model.MessageCitation
	if err := db.WithContext(ctx).First(&preserved, "id = ?", citation.ID).Error; err != nil {
		t.Fatal(err)
	}
	if preserved.KnowledgeBaseID != nil || preserved.DocumentID != nil || preserved.ChunkID != nil || preserved.Quote == "" {
		t.Fatalf("citation snapshot was not preserved correctly: %+v", preserved)
	}
	if err := db.WithContext(ctx).Delete(&message).Error; err != nil {
		t.Fatal(err)
	}
	var citationCount int64
	if err := db.WithContext(ctx).Model(&model.MessageCitation{}).Where("id = ?", citation.ID).Count(&citationCount).Error; err != nil {
		t.Fatal(err)
	}
	if citationCount != 0 {
		t.Fatalf("citation count after deleting message = %d, want 0", citationCount)
	}
}

func TestPostgresKnowledgeExactSearchLargeDataset(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	repositories := New(db)
	suffix := time.Now().UnixNano()
	user := model.User{Email: fmt.Sprintf("knowledge-large-%d@example.com", suffix), PasswordHash: "test-only", DisplayName: "Large Search"}
	if err := repositories.Users.Create(ctx, &user); err != nil {
		t.Fatal(err)
	}
	defer db.WithContext(ctx).Delete(&user)
	base := model.KnowledgeBase{Name: "大数据检索基线"}
	if err := repositories.Knowledge.CreateBase(ctx, user.ID, &base); err != nil {
		t.Fatal(err)
	}
	document := model.KnowledgeDocument{
		KnowledgeBaseID: base.ID, ObjectKey: fmt.Sprintf("knowledge/large-%d.txt", suffix),
		OriginalName: "large.txt", Extension: ".txt", MIMEType: "text/plain", Size: 1,
		Checksum: fmt.Sprintf("%064x", suffix), Status: model.KnowledgeDocumentStatusReady,
		EmbeddingProvider: "test", EmbeddingModel: "test-embedding", EmbeddingDimension: 3,
	}
	if err := repositories.Knowledge.CreateDocument(ctx, user.ID, &document); err != nil {
		t.Fatal(err)
	}
	chunks := make([]model.KnowledgeChunk, 1000)
	for index := range chunks {
		values := []float32{0, 1, 0}
		if index == 0 {
			values = []float32{1, 0, 0}
		}
		vector := pgvector.NewVector(values)
		chunks[index] = model.KnowledgeChunk{
			DocumentID: document.ID, ChunkIndex: index, Content: fmt.Sprintf("chunk-%04d", index),
			ContentHash: fmt.Sprintf("%064x", index+1), TokenCount: 4,
			Embedding: &vector, EmbeddingModel: "test-embedding", EmbeddingDimension: 3,
		}
	}
	if err := db.WithContext(ctx).CreateInBatches(&chunks, 100).Error; err != nil {
		t.Fatal(err)
	}
	query := pgvector.NewVector([]float32{1, 0, 0})
	rows, err := repositories.Knowledge.SearchChunksExact(ctx, user.ID, []string{base.ID}, "test", "test-embedding", 3, 20, query)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 20 || rows[0].ChunkIndex != 0 {
		t.Fatalf("large exact search returned %d rows, first=%+v", len(rows), rows[0])
	}
}
