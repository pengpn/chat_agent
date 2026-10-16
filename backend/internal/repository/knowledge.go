package repository

import (
	"context"
	"errors"
	"time"

	"github.com/example/agent-platform/backend/internal/database/model"
	"github.com/example/agent-platform/backend/internal/domain"
	pgvector "github.com/pgvector/pgvector-go"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// KnowledgeSearchRow 承载向量与关键词召回需要的来源数据和各路分数，
// 供 Service 融合排序及生成引用使用，不向 HTTP 层暴露原始向量。
type KnowledgeSearchRow struct {
	KnowledgeBaseID string
	DocumentID      string
	DocumentName    string
	ChunkID         string
	ChunkIndex      int
	Content         string
	ContentHash     string
	TokenCount      int64
	PageNumber      *int
	SectionTitle    string
	Metadata        model.JSONDocument
	Similarity      float64
	VectorRank      int
	KeywordRank     int
	KeywordScore    float64
	FusionScore     float64
}

// KnowledgeRepository 封装用户知识库的持久化边界。
// 用户数据的读写必须携带 userID，不能仅凭记录 UUID 访问。
type KnowledgeRepository interface {
	CreateBase(ctx context.Context, userID string, base *model.KnowledgeBase) error
	ListBasesByUser(ctx context.Context, userID string) ([]model.KnowledgeBase, error)
	FindBaseByIDForUser(ctx context.Context, userID, baseID string) (model.KnowledgeBase, error)
	UpdateBase(ctx context.Context, userID, baseID, name, description string) (model.KnowledgeBase, error)
	DeleteBase(ctx context.Context, userID, baseID string) error
	CreateDocument(ctx context.Context, userID string, document *model.KnowledgeDocument) error
	ListDocuments(ctx context.Context, userID, baseID string) ([]model.KnowledgeDocument, error)
	FindDocumentByIDForUser(ctx context.Context, userID, baseID, documentID string) (model.KnowledgeDocument, error)
	FindDocumentByChecksumForUser(ctx context.Context, userID, baseID, checksum string) (model.KnowledgeDocument, error)
	BeginDocumentIngestion(ctx context.Context, userID, baseID, documentID string, allowedStatuses []string) (model.KnowledgeDocument, error)
	AdvanceDocumentIngestion(ctx context.Context, userID, documentID string, version int64, fromStatus, toStatus, extractedText string) error
	CompleteDocumentIngestion(ctx context.Context, userID, documentID string, version int64, chunks []model.KnowledgeChunk, provider, embeddingModel string, dimension int, indexedAt time.Time) error
	FailDocumentIngestion(ctx context.Context, userID, documentID string, version int64, code, message string) error
	DeleteDocumentWithCleanup(ctx context.Context, userID, baseID, documentID string) error
	DeleteBaseWithCleanup(ctx context.Context, userID, baseID string) error
	ListPendingObjectCleanup(ctx context.Context, limit int) ([]model.KnowledgeObjectCleanup, error)
	CompleteObjectCleanup(ctx context.Context, id string) error
	FailObjectCleanup(ctx context.Context, id, message string) error
	ReplaceChunks(ctx context.Context, userID, documentID string, chunks []model.KnowledgeChunk) error
	ListChunks(ctx context.Context, userID, documentID string) ([]model.KnowledgeChunk, error)
	CountOwnedBases(ctx context.Context, userID string, baseIDs []string) (int64, error)
	SearchChunksExact(ctx context.Context, userID string, baseIDs []string, providerName, embeddingModel string, dimension, limit int, query pgvector.Vector) ([]KnowledgeSearchRow, error)
	SearchChunksKeyword(ctx context.Context, userID string, baseIDs []string, providerName, embeddingModel string, dimension, limit int, queryText string, query pgvector.Vector) ([]KnowledgeSearchRow, error)
}

type GormKnowledgeRepository struct{ db *gorm.DB }

func newKnowledgeRepository(db *gorm.DB) *GormKnowledgeRepository {
	return &GormKnowledgeRepository{db: db}
}

func (r *GormKnowledgeRepository) CreateBase(ctx context.Context, userID string, base *model.KnowledgeBase) error {
	base.UserID = userID
	return r.db.WithContext(ctx).Create(base).Error
}

func (r *GormKnowledgeRepository) ListBasesByUser(ctx context.Context, userID string) ([]model.KnowledgeBase, error) {
	items := make([]model.KnowledgeBase, 0)
	err := r.db.WithContext(ctx).Where("user_id = ?", userID).Order("updated_at DESC, id DESC").Find(&items).Error
	return items, err
}

func (r *GormKnowledgeRepository) FindBaseByIDForUser(ctx context.Context, userID, baseID string) (model.KnowledgeBase, error) {
	var base model.KnowledgeBase
	err := r.db.WithContext(ctx).Where("id = ? AND user_id = ?", baseID, userID).First(&base).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.KnowledgeBase{}, domain.ErrNotFound
	}
	return base, err
}

func (r *GormKnowledgeRepository) UpdateBase(ctx context.Context, userID, baseID, name, description string) (model.KnowledgeBase, error) {
	result := r.db.WithContext(ctx).Model(&model.KnowledgeBase{}).Where("id = ? AND user_id = ?", baseID, userID).
		Updates(map[string]any{"name": name, "description": description})
	if result.Error != nil {
		return model.KnowledgeBase{}, result.Error
	}
	if result.RowsAffected == 0 {
		return model.KnowledgeBase{}, domain.ErrNotFound
	}
	return r.FindBaseByIDForUser(ctx, userID, baseID)
}

func (r *GormKnowledgeRepository) DeleteBase(ctx context.Context, userID, baseID string) error {
	result := r.db.WithContext(ctx).Where("id = ? AND user_id = ?", baseID, userID).Delete(&model.KnowledgeBase{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *GormKnowledgeRepository) CreateDocument(ctx context.Context, userID string, document *model.KnowledgeDocument) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var base model.KnowledgeBase
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).
			Where("id = ? AND user_id = ?", document.KnowledgeBaseID, userID).First(&base).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrNotFound
			}
			return err
		}
		return tx.Create(document).Error
	})
	if isUniqueViolation(err) {
		return domain.ErrAlreadyExists
	}
	return err
}

func (r *GormKnowledgeRepository) ListDocuments(ctx context.Context, userID, baseID string) ([]model.KnowledgeDocument, error) {
	items := make([]model.KnowledgeDocument, 0)
	err := r.db.WithContext(ctx).Model(&model.KnowledgeDocument{}).
		Joins("JOIN knowledge_bases ON knowledge_bases.id = knowledge_documents.knowledge_base_id").
		Where("knowledge_documents.knowledge_base_id = ? AND knowledge_bases.user_id = ?", baseID, userID).
		Order("knowledge_documents.created_at DESC, knowledge_documents.id DESC").Find(&items).Error
	return items, err
}

func (r *GormKnowledgeRepository) FindDocumentByIDForUser(ctx context.Context, userID, baseID, documentID string) (model.KnowledgeDocument, error) {
	var item model.KnowledgeDocument
	err := r.db.WithContext(ctx).Model(&model.KnowledgeDocument{}).
		Joins("JOIN knowledge_bases ON knowledge_bases.id = knowledge_documents.knowledge_base_id").
		Where("knowledge_documents.id = ? AND knowledge_documents.knowledge_base_id = ? AND knowledge_bases.user_id = ?", documentID, baseID, userID).
		First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.KnowledgeDocument{}, domain.ErrNotFound
	}
	return item, err
}

func (r *GormKnowledgeRepository) FindDocumentByChecksumForUser(ctx context.Context, userID, baseID, checksum string) (model.KnowledgeDocument, error) {
	var item model.KnowledgeDocument
	err := r.db.WithContext(ctx).Model(&model.KnowledgeDocument{}).
		Joins("JOIN knowledge_bases ON knowledge_bases.id = knowledge_documents.knowledge_base_id").
		Where("knowledge_documents.knowledge_base_id = ? AND knowledge_documents.checksum = ? AND knowledge_bases.user_id = ?", baseID, checksum, userID).
		First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.KnowledgeDocument{}, domain.ErrNotFound
	}
	return item, err
}

func (r *GormKnowledgeRepository) BeginDocumentIngestion(ctx context.Context, userID, baseID, documentID string, allowedStatuses []string) (model.KnowledgeDocument, error) {
	var document model.KnowledgeDocument
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Model(&model.KnowledgeDocument{}).
			Where("id = ? AND knowledge_base_id = ? AND EXISTS (SELECT 1 FROM knowledge_bases WHERE knowledge_bases.id = knowledge_documents.knowledge_base_id AND knowledge_bases.user_id = ?)", documentID, baseID, userID).
			First(&document).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrNotFound
			}
			return err
		}
		allowed := false
		for _, status := range allowedStatuses {
			if document.Status == status {
				allowed = true
				break
			}
		}
		if !allowed {
			return domain.ErrInvalid
		}
		document.IngestionVersion++
		document.Status = model.KnowledgeDocumentStatusExtracting
		document.ErrorCode = ""
		document.ErrorMessage = ""
		return tx.Model(&model.KnowledgeDocument{}).Where("id = ?", document.ID).Updates(map[string]any{
			"ingestion_version": document.IngestionVersion,
			"status":            document.Status,
			"error_code":        "",
			"error_message":     "",
		}).Error
	})
	return document, err
}

func (r *GormKnowledgeRepository) AdvanceDocumentIngestion(ctx context.Context, userID, documentID string, version int64, fromStatus, toStatus, extractedText string) error {
	updates := map[string]any{"status": toStatus}
	if extractedText != "" {
		updates["extracted_text"] = extractedText
	}
	result := r.db.WithContext(ctx).Model(&model.KnowledgeDocument{}).
		Where("id = ? AND ingestion_version = ? AND status = ? AND EXISTS (SELECT 1 FROM knowledge_bases WHERE knowledge_bases.id = knowledge_documents.knowledge_base_id AND knowledge_bases.user_id = ?)", documentID, version, fromStatus, userID).
		Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return domain.ErrInvalid
	}
	return nil
}

func (r *GormKnowledgeRepository) CompleteDocumentIngestion(ctx context.Context, userID, documentID string, version int64, chunks []model.KnowledgeChunk, providerName, embeddingModel string, dimension int, indexedAt time.Time) error {
	prepareKnowledgeKeywords(chunks)
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var document model.KnowledgeDocument
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Model(&model.KnowledgeDocument{}).
			Where("id = ? AND ingestion_version = ? AND status = ? AND EXISTS (SELECT 1 FROM knowledge_bases WHERE knowledge_bases.id = knowledge_documents.knowledge_base_id AND knowledge_bases.user_id = ?)", documentID, version, model.KnowledgeDocumentStatusEmbedding, userID).
			First(&document).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrInvalid
			}
			return err
		}
		if err := tx.Where("document_id = ?", documentID).Delete(&model.KnowledgeChunk{}).Error; err != nil {
			return err
		}
		for index := range chunks {
			chunks[index].DocumentID = documentID
		}
		if len(chunks) > 0 {
			if err := tx.Create(&chunks).Error; err != nil {
				return err
			}
		}
		return tx.Model(&model.KnowledgeDocument{}).Where("id = ?", documentID).Updates(map[string]any{
			"status": model.KnowledgeDocumentStatusReady, "chunk_count": len(chunks),
			"embedding_provider": providerName, "embedding_model": embeddingModel,
			"embedding_dimension": dimension, "indexed_at": indexedAt,
			"error_code": "", "error_message": "",
		}).Error
	})
}

func (r *GormKnowledgeRepository) FailDocumentIngestion(ctx context.Context, userID, documentID string, version int64, code, message string) error {
	result := r.db.WithContext(ctx).Model(&model.KnowledgeDocument{}).
		Where("id = ? AND ingestion_version = ? AND EXISTS (SELECT 1 FROM knowledge_bases WHERE knowledge_bases.id = knowledge_documents.knowledge_base_id AND knowledge_bases.user_id = ?)", documentID, version, userID).
		Updates(map[string]any{"status": model.KnowledgeDocumentStatusFailed, "error_code": code, "error_message": message})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return domain.ErrInvalid
	}
	return nil
}

func (r *GormKnowledgeRepository) DeleteDocumentWithCleanup(ctx context.Context, userID, baseID, documentID string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var document model.KnowledgeDocument
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Model(&model.KnowledgeDocument{}).
			Where("id = ? AND knowledge_base_id = ? AND EXISTS (SELECT 1 FROM knowledge_bases WHERE knowledge_bases.id = knowledge_documents.knowledge_base_id AND knowledge_bases.user_id = ?)", documentID, baseID, userID).
			First(&document).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrNotFound
			}
			return err
		}
		if err := enqueueKnowledgeObjectCleanup(tx, []string{document.ObjectKey}); err != nil {
			return err
		}
		return tx.Delete(&document).Error
	})
}

func (r *GormKnowledgeRepository) DeleteBaseWithCleanup(ctx context.Context, userID, baseID string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var base model.KnowledgeBase
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ?", baseID, userID).First(&base).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrNotFound
			}
			return err
		}
		var keys []string
		if err := tx.Model(&model.KnowledgeDocument{}).Where("knowledge_base_id = ?", baseID).Pluck("object_key", &keys).Error; err != nil {
			return err
		}
		if err := enqueueKnowledgeObjectCleanup(tx, keys); err != nil {
			return err
		}
		return tx.Delete(&base).Error
	})
}

func enqueueKnowledgeObjectCleanup(tx *gorm.DB, keys []string) error {
	for _, key := range keys {
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.KnowledgeObjectCleanup{ObjectKey: key}).Error; err != nil {
			return err
		}
	}
	return nil
}

func (r *GormKnowledgeRepository) ListPendingObjectCleanup(ctx context.Context, limit int) ([]model.KnowledgeObjectCleanup, error) {
	if limit <= 0 {
		limit = 100
	}
	items := make([]model.KnowledgeObjectCleanup, 0)
	err := r.db.WithContext(ctx).Order("created_at ASC").Limit(limit).Find(&items).Error
	return items, err
}

func (r *GormKnowledgeRepository) CompleteObjectCleanup(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Delete(&model.KnowledgeObjectCleanup{}, "id = ?", id).Error
}

func (r *GormKnowledgeRepository) FailObjectCleanup(ctx context.Context, id, message string) error {
	return r.db.WithContext(ctx).Model(&model.KnowledgeObjectCleanup{}).Where("id = ?", id).
		Updates(map[string]any{"attempts": gorm.Expr("attempts + 1"), "last_error": message}).Error
}

func (r *GormKnowledgeRepository) ReplaceChunks(ctx context.Context, userID, documentID string, chunks []model.KnowledgeChunk) error {
	prepareKnowledgeKeywords(chunks)
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var document model.KnowledgeDocument
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Model(&model.KnowledgeDocument{}).
			Joins("JOIN knowledge_bases ON knowledge_bases.id = knowledge_documents.knowledge_base_id").
			Where("knowledge_documents.id = ? AND knowledge_bases.user_id = ?", documentID, userID).First(&document).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrNotFound
			}
			return err
		}
		if err := tx.Where("document_id = ?", documentID).Delete(&model.KnowledgeChunk{}).Error; err != nil {
			return err
		}
		for index := range chunks {
			chunks[index].DocumentID = documentID
		}
		if len(chunks) > 0 {
			if err := tx.Create(&chunks).Error; err != nil {
				return err
			}
		}
		return tx.Model(&model.KnowledgeDocument{}).Where("id = ?", documentID).Update("chunk_count", len(chunks)).Error
	})
}

func (r *GormKnowledgeRepository) ListChunks(ctx context.Context, userID, documentID string) ([]model.KnowledgeChunk, error) {
	items := make([]model.KnowledgeChunk, 0)
	err := r.db.WithContext(ctx).Model(&model.KnowledgeChunk{}).
		Joins("JOIN knowledge_documents ON knowledge_documents.id = knowledge_chunks.document_id").
		Joins("JOIN knowledge_bases ON knowledge_bases.id = knowledge_documents.knowledge_base_id").
		Where("knowledge_chunks.document_id = ? AND knowledge_bases.user_id = ?", documentID, userID).
		Order("knowledge_chunks.chunk_index ASC").Find(&items).Error
	return items, err
}

func (r *GormKnowledgeRepository) CountOwnedBases(ctx context.Context, userID string, baseIDs []string) (int64, error) {
	if len(baseIDs) == 0 {
		return 0, nil
	}
	var count int64
	err := r.db.WithContext(ctx).Model(&model.KnowledgeBase{}).
		Where("user_id = ? AND id IN ?", userID, baseIDs).
		Distinct("id").Count(&count).Error
	return count, err
}

// SearchChunksExact 使用精确余弦距离排序建立基线。
// 是否增加 HNSW 近似索引，需要依据后续召回率和延迟实验决定。
func (r *GormKnowledgeRepository) SearchChunksExact(ctx context.Context, userID string, baseIDs []string, providerName, embeddingModel string, dimension, limit int, query pgvector.Vector) ([]KnowledgeSearchRow, error) {
	if len(baseIDs) == 0 || limit <= 0 {
		return []KnowledgeSearchRow{}, nil
	}
	items := make([]KnowledgeSearchRow, 0, limit)
	err := r.db.WithContext(ctx).Raw(`
		WITH query_vector AS (SELECT ?::vector AS embedding)
		SELECT
			knowledge_documents.knowledge_base_id,
			knowledge_chunks.document_id,
			knowledge_documents.original_name AS document_name,
			knowledge_chunks.id AS chunk_id,
			knowledge_chunks.chunk_index,
			knowledge_chunks.content,
			knowledge_chunks.content_hash,
			knowledge_chunks.token_count,
			knowledge_chunks.page_number,
			knowledge_chunks.section_title,
			knowledge_chunks.metadata,
			1 - (knowledge_chunks.embedding <=> query_vector.embedding) AS similarity
		FROM knowledge_chunks
		JOIN knowledge_documents ON knowledge_documents.id = knowledge_chunks.document_id
		JOIN knowledge_bases ON knowledge_bases.id = knowledge_documents.knowledge_base_id
		CROSS JOIN query_vector
		WHERE knowledge_bases.user_id = ?
			AND knowledge_documents.knowledge_base_id IN ?
			AND knowledge_documents.status = ?
			AND knowledge_documents.embedding_provider = ?
			AND knowledge_documents.embedding_model = ?
			AND knowledge_documents.embedding_dimension = ?
			AND knowledge_chunks.embedding IS NOT NULL
			AND knowledge_chunks.embedding_model = ?
			AND knowledge_chunks.embedding_dimension = ?
		ORDER BY
			knowledge_chunks.embedding <=> query_vector.embedding ASC,
			knowledge_chunks.document_id ASC,
			knowledge_chunks.chunk_index ASC,
			knowledge_chunks.id ASC
		LIMIT ?
	`, query, userID, baseIDs, model.KnowledgeDocumentStatusReady, providerName, embeddingModel, dimension, embeddingModel, dimension, limit).Scan(&items).Error
	return items, err
}

var _ KnowledgeRepository = (*GormKnowledgeRepository)(nil)
