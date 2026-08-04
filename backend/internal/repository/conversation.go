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

// ConversationRepository defines the conversation persistence operations used
// by services.
type ConversationRepository interface {
	Create(ctx context.Context, conversation *model.Conversation) error
	ListByUserID(ctx context.Context, userID string) ([]model.Conversation, error)
	FindByIDForUser(ctx context.Context, userID, id string) (model.Conversation, error)
	UpdateTitle(ctx context.Context, userID, id, title string) (model.Conversation, error)
	Delete(ctx context.Context, userID, id string) error
	Touch(ctx context.Context, id string) error
	SaveSummaryVersion(ctx context.Context, userID string, version *model.ConversationSummaryVersion, activate bool) error
}

// GormConversationRepository is the PostgreSQL/GORM implementation of
// ConversationRepository.
type GormConversationRepository struct{ db *gorm.DB }

func newConversationRepository(db *gorm.DB) *GormConversationRepository {
	return &GormConversationRepository{db: db}
}

func (r *GormConversationRepository) Create(ctx context.Context, conversation *model.Conversation) error {
	return r.db.WithContext(ctx).Create(conversation).Error
}

func (r *GormConversationRepository) ListByUserID(ctx context.Context, userID string) ([]model.Conversation, error) {
	items := make([]model.Conversation, 0)
	err := r.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("updated_at DESC").
		Find(&items).Error
	return items, err
}

func (r *GormConversationRepository) FindByIDForUser(ctx context.Context, userID, id string) (model.Conversation, error) {
	var item model.Conversation
	err := r.db.WithContext(ctx).Where("id = ? AND user_id = ?", id, userID).First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.Conversation{}, domain.ErrNotFound
	}
	return item, err
}

func (r *GormConversationRepository) UpdateTitle(ctx context.Context, userID, id, title string) (model.Conversation, error) {
	item, err := r.FindByIDForUser(ctx, userID, id)
	if err != nil {
		return model.Conversation{}, err
	}
	item.Title = title
	if err := r.db.WithContext(ctx).Save(&item).Error; err != nil {
		return model.Conversation{}, err
	}
	return item, nil
}

func (r *GormConversationRepository) Delete(ctx context.Context, userID, id string) error {
	result := r.db.WithContext(ctx).Where("id = ? AND user_id = ?", id, userID).Delete(&model.Conversation{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *GormConversationRepository) Touch(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).
		Model(&model.Conversation{}).
		Where("id = ?", id).
		Update("updated_at", time.Now()).Error
}

func (r *GormConversationRepository) SaveSummaryVersion(ctx context.Context, userID string, version *model.ConversationSummaryVersion, activate bool) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var conversation model.Conversation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND user_id = ?", version.ConversationID, userID).First(&conversation).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrNotFound
			}
			return err
		}
		var latest int64
		if err := tx.Model(&model.ConversationSummaryVersion{}).
			Where("conversation_id = ?", version.ConversationID).
			Select("COALESCE(MAX(version), 0)").Scan(&latest).Error; err != nil {
			return err
		}
		version.Version = latest + 1
		if err := tx.Create(version).Error; err != nil {
			return err
		}
		if !activate {
			return nil
		}
		return tx.Model(&model.Conversation{}).Where("id = ?", version.ConversationID).Updates(map[string]any{
			"summary": version.Summary, "summary_through_message_id": version.ThroughMessageID, "summary_updated_at": time.Now(),
		}).Error
	})
}
