package repository

import (
	"context"
	"errors"

	"github.com/example/agent-platform/backend/internal/database/model"
	"github.com/example/agent-platform/backend/internal/domain"
	"gorm.io/gorm"
)

// AttachmentRepository keeps every query user-scoped; callers cannot fetch an
// attachment by UUID alone.
type AttachmentRepository interface {
	Create(ctx context.Context, attachment *model.Attachment) error
	FindByIDForUser(ctx context.Context, userID, conversationID, id string) (model.Attachment, error)
	FindManyForUser(ctx context.Context, userID, conversationID string, ids []string) ([]model.Attachment, error)
	ListObjectKeysByConversation(ctx context.Context, userID, conversationID string) ([]string, error)
	UpdateParsing(ctx context.Context, userID, conversationID, id, status, text, errorCode, errorMessage string) (model.Attachment, error)
	DeletePending(ctx context.Context, userID, conversationID, id string) (model.Attachment, error)
	ClaimForMessage(ctx context.Context, tx *gorm.DB, userID, conversationID, messageID string, ids []string) error
}

func (r *GormAttachmentRepository) ClaimForMessage(ctx context.Context, tx *gorm.DB, userID, conversationID, messageID string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	result := tx.WithContext(ctx).Model(&model.Attachment{}).
		Where("id IN ? AND user_id = ? AND conversation_id = ? AND message_id IS NULL AND status = ?", ids, userID, conversationID, model.AttachmentStatusReady).
		Update("message_id", messageID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != int64(len(ids)) {
		return domain.ErrNotFound
	}
	return nil
}

type GormAttachmentRepository struct{ db *gorm.DB }

func newAttachmentRepository(db *gorm.DB) *GormAttachmentRepository {
	return &GormAttachmentRepository{db: db}
}

func (r *GormAttachmentRepository) Create(ctx context.Context, attachment *model.Attachment) error {
	return r.db.WithContext(ctx).Create(attachment).Error
}

func (r *GormAttachmentRepository) FindByIDForUser(ctx context.Context, userID, conversationID, id string) (model.Attachment, error) {
	var item model.Attachment
	err := r.db.WithContext(ctx).Where("id = ? AND user_id = ? AND conversation_id = ?", id, userID, conversationID).First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.Attachment{}, domain.ErrNotFound
	}
	return item, err
}

func (r *GormAttachmentRepository) FindManyForUser(ctx context.Context, userID, conversationID string, ids []string) ([]model.Attachment, error) {
	items := make([]model.Attachment, 0, len(ids))
	if len(ids) == 0 {
		return items, nil
	}
	err := r.db.WithContext(ctx).Where("user_id = ? AND conversation_id = ? AND id IN ?", userID, conversationID, ids).Find(&items).Error
	return items, err
}

func (r *GormAttachmentRepository) ListObjectKeysByConversation(ctx context.Context, userID, conversationID string) ([]string, error) {
	var keys []string
	err := r.db.WithContext(ctx).Model(&model.Attachment{}).Where("user_id = ? AND conversation_id = ?", userID, conversationID).Pluck("object_key", &keys).Error
	return keys, err
}

func (r *GormAttachmentRepository) UpdateParsing(ctx context.Context, userID, conversationID, id, status, extractedText, errorCode, errorMessage string) (model.Attachment, error) {
	err := r.db.WithContext(ctx).Model(&model.Attachment{}).Where("id = ? AND user_id = ? AND conversation_id = ?", id, userID, conversationID).Updates(map[string]any{
		"status": status, "extracted_text": extractedText, "error_code": errorCode, "error_message": errorMessage,
	}).Error
	if err != nil {
		return model.Attachment{}, err
	}
	var item model.Attachment
	err = r.db.WithContext(ctx).First(&item, "id = ? AND user_id = ? AND conversation_id = ?", id, userID, conversationID).Error
	return item, err
}

func (r *GormAttachmentRepository) DeletePending(ctx context.Context, userID, conversationID, id string) (model.Attachment, error) {
	var item model.Attachment
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("id = ? AND user_id = ? AND conversation_id = ? AND message_id IS NULL", id, userID, conversationID).First(&item).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrNotFound
			}
			return err
		}
		return tx.Delete(&item).Error
	})
	return item, err
}
