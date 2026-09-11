package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/example/agent-platform/backend/internal/database/model"
	"github.com/example/agent-platform/backend/internal/domain"
	"github.com/example/agent-platform/backend/internal/embeddinggateway"
	"github.com/example/agent-platform/backend/internal/objectstore"
)

type fakeEmbeddingGateway struct {
	mu    sync.Mutex
	calls int
}

func (f *fakeEmbeddingGateway) EmbedDocuments(_ context.Context, request embeddinggateway.Request) (embeddinggateway.Result, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	vectors := make([]embeddinggateway.Vector, len(request.Texts))
	for index := range vectors {
		vectors[index] = embeddinggateway.Vector{Index: index, Values: []float32{float32(index + 1), 1, 0}}
	}
	return embeddinggateway.Result{Provider: "test", Model: "embedding-v1", Dimension: 3, Vectors: vectors}, nil
}

func (f *fakeEmbeddingGateway) EmbedQuery(context.Context, embeddinggateway.Request) (embeddinggateway.Result, error) {
	return embeddinggateway.Result{}, nil
}

type fakeKnowledgeRepository struct {
	mu        sync.Mutex
	base      model.KnowledgeBase
	documents map[string]model.KnowledgeDocument
	chunks    map[string][]model.KnowledgeChunk
	cleanups  []model.KnowledgeObjectCleanup
}

func newFakeKnowledgeRepository() *fakeKnowledgeRepository {
	return &fakeKnowledgeRepository{
		base:      model.KnowledgeBase{ID: "base-1", UserID: "user-1", Name: "测试知识库"},
		documents: make(map[string]model.KnowledgeDocument), chunks: make(map[string][]model.KnowledgeChunk),
	}
}

func (r *fakeKnowledgeRepository) CreateBase(_ context.Context, userID string, base *model.KnowledgeBase) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	base.ID, base.UserID = "base-1", userID
	r.base = *base
	return nil
}
func (r *fakeKnowledgeRepository) ListBasesByUser(_ context.Context, userID string) ([]model.KnowledgeBase, error) {
	if userID != r.base.UserID {
		return []model.KnowledgeBase{}, nil
	}
	return []model.KnowledgeBase{r.base}, nil
}
func (r *fakeKnowledgeRepository) FindBaseByIDForUser(_ context.Context, userID, baseID string) (model.KnowledgeBase, error) {
	if userID != r.base.UserID || baseID != r.base.ID {
		return model.KnowledgeBase{}, domain.ErrNotFound
	}
	return r.base, nil
}
func (r *fakeKnowledgeRepository) UpdateBase(_ context.Context, userID, baseID, name, description string) (model.KnowledgeBase, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if userID != r.base.UserID || baseID != r.base.ID {
		return model.KnowledgeBase{}, domain.ErrNotFound
	}
	r.base.Name, r.base.Description = name, description
	return r.base, nil
}
func (r *fakeKnowledgeRepository) DeleteBase(context.Context, string, string) error { return nil }
func (r *fakeKnowledgeRepository) CreateDocument(_ context.Context, userID string, document *model.KnowledgeDocument) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if userID != r.base.UserID || document.KnowledgeBaseID != r.base.ID {
		return domain.ErrNotFound
	}
	for _, existing := range r.documents {
		if existing.KnowledgeBaseID == document.KnowledgeBaseID && existing.Checksum == document.Checksum {
			return domain.ErrAlreadyExists
		}
	}
	r.documents[document.ID] = *document
	return nil
}
func (r *fakeKnowledgeRepository) ListDocuments(_ context.Context, userID, baseID string) ([]model.KnowledgeDocument, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if userID != r.base.UserID || baseID != r.base.ID {
		return nil, domain.ErrNotFound
	}
	items := make([]model.KnowledgeDocument, 0, len(r.documents))
	for _, item := range r.documents {
		items = append(items, item)
	}
	return items, nil
}
func (r *fakeKnowledgeRepository) FindDocumentByIDForUser(_ context.Context, userID, baseID, documentID string) (model.KnowledgeDocument, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.documents[documentID]
	if !ok || userID != r.base.UserID || baseID != r.base.ID {
		return model.KnowledgeDocument{}, domain.ErrNotFound
	}
	return item, nil
}
func (r *fakeKnowledgeRepository) FindDocumentByChecksumForUser(_ context.Context, userID, baseID, checksum string) (model.KnowledgeDocument, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, item := range r.documents {
		if userID == r.base.UserID && baseID == r.base.ID && item.Checksum == checksum {
			return item, nil
		}
	}
	return model.KnowledgeDocument{}, domain.ErrNotFound
}
func (r *fakeKnowledgeRepository) BeginDocumentIngestion(_ context.Context, userID, baseID, documentID string, allowed []string) (model.KnowledgeDocument, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.documents[documentID]
	if !ok || userID != r.base.UserID || baseID != r.base.ID {
		return model.KnowledgeDocument{}, domain.ErrNotFound
	}
	for _, status := range allowed {
		if item.Status == status {
			item.Status = model.KnowledgeDocumentStatusExtracting
			item.IngestionVersion++
			r.documents[documentID] = item
			return item, nil
		}
	}
	return model.KnowledgeDocument{}, domain.ErrInvalid
}
func (r *fakeKnowledgeRepository) AdvanceDocumentIngestion(_ context.Context, userID, documentID string, version int64, from, to, extracted string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.documents[documentID]
	if !ok || userID != r.base.UserID || item.IngestionVersion != version || item.Status != from {
		return domain.ErrInvalid
	}
	item.Status = to
	if extracted != "" {
		item.ExtractedText = extracted
	}
	r.documents[documentID] = item
	return nil
}
func (r *fakeKnowledgeRepository) CompleteDocumentIngestion(_ context.Context, userID, documentID string, version int64, chunks []model.KnowledgeChunk, provider, embeddingModel string, dimension int, indexedAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.documents[documentID]
	if !ok || userID != r.base.UserID || item.IngestionVersion != version || item.Status != model.KnowledgeDocumentStatusEmbedding {
		return domain.ErrInvalid
	}
	item.Status, item.ChunkCount = model.KnowledgeDocumentStatusReady, len(chunks)
	item.EmbeddingProvider, item.EmbeddingModel, item.EmbeddingDimension, item.IndexedAt = provider, embeddingModel, dimension, &indexedAt
	r.documents[documentID], r.chunks[documentID] = item, chunks
	return nil
}
func (r *fakeKnowledgeRepository) FailDocumentIngestion(_ context.Context, userID, documentID string, version int64, code, message string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.documents[documentID]
	if !ok || userID != r.base.UserID || item.IngestionVersion != version {
		return domain.ErrInvalid
	}
	item.Status, item.ErrorCode, item.ErrorMessage = model.KnowledgeDocumentStatusFailed, code, message
	r.documents[documentID] = item
	return nil
}
func (r *fakeKnowledgeRepository) DeleteDocumentWithCleanup(context.Context, string, string, string) error {
	return nil
}
func (r *fakeKnowledgeRepository) DeleteBaseWithCleanup(context.Context, string, string) error {
	return nil
}
func (r *fakeKnowledgeRepository) ListPendingObjectCleanup(context.Context, int) ([]model.KnowledgeObjectCleanup, error) {
	return nil, nil
}
func (r *fakeKnowledgeRepository) CompleteObjectCleanup(context.Context, string) error { return nil }
func (r *fakeKnowledgeRepository) FailObjectCleanup(context.Context, string, string) error {
	return nil
}
func (r *fakeKnowledgeRepository) ReplaceChunks(_ context.Context, _ string, documentID string, chunks []model.KnowledgeChunk) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.chunks[documentID] = chunks
	return nil
}
func (r *fakeKnowledgeRepository) ListChunks(_ context.Context, userID, documentID string) ([]model.KnowledgeChunk, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if userID != r.base.UserID {
		return nil, nil
	}
	return r.chunks[documentID], nil
}

func TestKnowledgeUploadIsIdempotentAndPersistsReadyChunks(t *testing.T) {
	repository := newFakeKnowledgeRepository()
	embeddings := &fakeEmbeddingGateway{}
	service := NewKnowledge(repository, objectstore.NewMemory(), embeddings, embeddinggateway.HeuristicTokenEstimator{}, KnowledgeConfig{
		ChunkTargetTokens: 10, ChunkOverlap: 2, ChunkMinTokens: 1, IngestionTimeout: time.Second,
	})
	data := []byte("# 退款规则\n退款可以在七日内申请。\n企业客户请联系客户成功经理。")
	first, err := service.Upload(context.Background(), "user-1", "base-1", "规则.txt", data)
	if err != nil || !first.Created {
		t.Fatalf("Upload() = %+v, %v", first, err)
	}
	ready := waitForKnowledgeStatus(t, repository, first.Document.ID, model.KnowledgeDocumentStatusReady)
	if ready.ChunkCount < 2 || ready.EmbeddingModel != "embedding-v1" || ready.IngestionVersion != 1 {
		t.Fatalf("unexpected ready document: %+v", ready)
	}
	second, err := service.Upload(context.Background(), "user-1", "base-1", "副本.txt", data)
	if err != nil || second.Created || second.Document.ID != first.Document.ID {
		t.Fatalf("duplicate Upload() = %+v, %v", second, err)
	}
	embeddings.mu.Lock()
	calls := embeddings.calls
	embeddings.mu.Unlock()
	if calls != 1 {
		t.Fatalf("embedding calls = %d, want 1", calls)
	}
}

func TestCreateKnowledgeBaseReturnsRepositoryGeneratedID(t *testing.T) {
	repository := newFakeKnowledgeRepository()
	service := NewKnowledge(repository, objectstore.NewMemory(), nil, nil, KnowledgeConfig{})
	base, err := service.CreateBase(context.Background(), "user-1", "  产品资料  ", "  销售手册  ")
	if err != nil {
		t.Fatal(err)
	}
	if base.ID != "base-1" || base.Name != "产品资料" || base.Description != "销售手册" {
		t.Fatalf("unexpected knowledge base: %+v", base)
	}
}

func TestKnowledgeConcurrentRetryAllowsOneAttempt(t *testing.T) {
	repository := newFakeKnowledgeRepository()
	store := objectstore.NewMemory()
	document := model.KnowledgeDocument{ID: "doc-1", KnowledgeBaseID: "base-1", ObjectKey: "knowledge/doc-1.txt", OriginalName: "doc.txt", Extension: ".txt", MIMEType: "text/plain", Checksum: "checksum", Status: model.KnowledgeDocumentStatusFailed}
	repository.documents[document.ID] = document
	if err := store.Put(context.Background(), document.ObjectKey, document.MIMEType, []byte("可重试内容")); err != nil {
		t.Fatal(err)
	}
	service := NewKnowledge(repository, store, &fakeEmbeddingGateway{}, embeddinggateway.HeuristicTokenEstimator{}, KnowledgeConfig{IngestionTimeout: time.Second})
	start := make(chan struct{})
	errorsOut := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			_, err := service.Retry(context.Background(), "user-1", "base-1", document.ID)
			errorsOut <- err
		}()
	}
	close(start)
	firstErr, secondErr := <-errorsOut, <-errorsOut
	busyCount := 0
	for _, err := range []error{firstErr, secondErr} {
		if errors.Is(err, ErrKnowledgeDocumentBusy) {
			busyCount++
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if busyCount != 1 {
		t.Fatalf("busy errors = %d, want 1 (%v, %v)", busyCount, firstErr, secondErr)
	}
	ready := waitForKnowledgeStatus(t, repository, document.ID, model.KnowledgeDocumentStatusReady)
	if ready.IngestionVersion != 1 {
		t.Fatalf("ingestion version = %d, want 1", ready.IngestionVersion)
	}
}

func TestKnowledgeBadDOCXTransitionsToFailed(t *testing.T) {
	// A ZIP with a malformed document.xml passes MIME validation but fails extraction.
	data := buildTestDOCX(t, `<w:document xmlns:w="urn:test"><w:body><w:p>`)
	repository := newFakeKnowledgeRepository()
	service := NewKnowledge(repository, objectstore.NewMemory(), &fakeEmbeddingGateway{}, embeddinggateway.HeuristicTokenEstimator{}, KnowledgeConfig{IngestionTimeout: time.Second})
	result, err := service.Upload(context.Background(), "user-1", "base-1", "损坏.docx", data)
	if err != nil {
		t.Fatal(err)
	}
	failed := waitForKnowledgeStatus(t, repository, result.Document.ID, model.KnowledgeDocumentStatusFailed)
	if failed.ErrorCode != "invalid_document" {
		t.Fatalf("error code = %q", failed.ErrorCode)
	}
}

func TestKnowledgeChunkerPreservesSectionOffsetsAndOverlap(t *testing.T) {
	document := assembleDocument([]rawDocumentBlock{{text: "# 第一章", sectionTitle: "第一章"}, {text: "abcdefghijklmnopqrst", sectionTitle: "第一章"}})
	chunks := buildKnowledgeChunks(document, embeddinggateway.HeuristicTokenEstimator{}, knowledgeChunkConfig{TargetTokens: 4, OverlapTokens: 1, MinTokens: 1})
	if len(chunks) < 2 {
		t.Fatalf("chunk count = %d", len(chunks))
	}
	for index, chunk := range chunks {
		if chunk.Content == "" || chunk.TokenCount <= 0 || len(chunk.Metadata) == 0 {
			t.Fatalf("invalid chunk %d: %+v", index, chunk)
		}
		if chunk.SectionTitle != "第一章" {
			t.Fatalf("section = %q", chunk.SectionTitle)
		}
	}
}

func TestExtractStructuredPDFPreservesPageNumber(t *testing.T) {
	document, code, err := extractStructuredDocument(".pdf", buildTestPDF())
	if err != nil {
		t.Fatalf("extract PDF code=%q error=%v", code, err)
	}
	if len(document.Blocks) == 0 || document.Blocks[0].PageNumber == nil || *document.Blocks[0].PageNumber != 1 {
		t.Fatalf("PDF page metadata was not preserved: %+v", document.Blocks)
	}
	if !strings.Contains(document.Text, "Hello RAG") {
		t.Fatalf("unexpected PDF text: %q", document.Text)
	}
}

func buildTestPDF() []byte {
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	stream := "BT /F1 12 Tf 72 720 Td (Hello RAG) Tj ET"
	objects = append(objects, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream))
	var output bytes.Buffer
	output.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects)+1)
	for index, object := range objects {
		offsets[index+1] = output.Len()
		fmt.Fprintf(&output, "%d 0 obj\n%s\nendobj\n", index+1, object)
	}
	xref := output.Len()
	fmt.Fprintf(&output, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for index := 1; index < len(offsets); index++ {
		fmt.Fprintf(&output, "%010d 00000 n \n", offsets[index])
	}
	fmt.Fprintf(&output, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return output.Bytes()
}

func waitForKnowledgeStatus(t *testing.T, repository *fakeKnowledgeRepository, documentID, status string) model.KnowledgeDocument {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		repository.mu.Lock()
		item := repository.documents[documentID]
		repository.mu.Unlock()
		if item.Status == status {
			return item
		}
		time.Sleep(5 * time.Millisecond)
	}
	repository.mu.Lock()
	item := repository.documents[documentID]
	repository.mu.Unlock()
	t.Fatalf("document status = %q, want %q", item.Status, status)
	return model.KnowledgeDocument{}
}
