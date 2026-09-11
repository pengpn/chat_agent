package repository

import (
	"context"
	"errors"
	"time"

	"github.com/example/agent-platform/backend/internal/database/model"
	"github.com/example/agent-platform/backend/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// KnowledgeRepository is the persistence boundary for user-owned knowledge.
// Every read and mutation receives userID so callers cannot access records by
// guessable UUID alone.
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

var _ KnowledgeRepository = (*GormKnowledgeRepository)(nil)
