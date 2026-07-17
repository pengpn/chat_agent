package repository

import (
	"context"
	"errors"

	"github.com/example/agent-platform/backend/internal/database/model"
	"github.com/example/agent-platform/backend/internal/domain"
	"gorm.io/gorm"
)

// MessageRepository defines the message persistence operations used by
// services.
type MessageRepository interface {
	ListByConversationForUser(ctx context.Context, userID, conversationID string) ([]model.Message, error)
	Create(ctx context.Context, message *model.Message) error
	Finalize(ctx context.Context, id, content, status string, usage model.MessageUsage) (model.Message, error)
}

// GormMessageRepository is the PostgreSQL/GORM implementation of
// MessageRepository.
type GormMessageRepository struct{ db *gorm.DB }

func newMessageRepository(db *gorm.DB) *GormMessageRepository { return &GormMessageRepository{db: db} }

func (r *GormMessageRepository) ListByConversationForUser(ctx context.Context, userID, conversationID string) ([]model.Message, error) {
	items := make([]model.Message, 0)
	err := r.db.WithContext(ctx).
		Model(&model.Message{}).
		Select("messages.*").
		Joins("JOIN conversations ON conversations.id = messages.conversation_id").
		Where("messages.conversation_id = ? AND conversations.user_id = ?", conversationID, userID).
		Order("messages.created_at ASC").
		Find(&items).Error
	return items, err
}

func (r *GormMessageRepository) Create(ctx context.Context, message *model.Message) error {
	return r.db.WithContext(ctx).Create(message).Error
}

func (r *GormMessageRepository) Finalize(ctx context.Context, id, content, status string, usage model.MessageUsage) (model.Message, error) {
	var item model.Message
	err := r.db.WithContext(ctx).First(&item, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.Message{}, domain.ErrNotFound
	}
	if err != nil {
		return model.Message{}, err
	}
	item.Content = content
	item.Status = status
	item.InputTokens = usage.InputTokens
	item.OutputTokens = usage.OutputTokens
	item.TotalTokens = usage.TotalTokens
	item.UsageReported = usage.UsageReported
	item.CostNanoUSD = usage.CostNanoUSD
	item.CostKnown = usage.CostKnown
	if err := r.db.WithContext(ctx).Save(&item).Error; err != nil {
		return model.Message{}, err
	}
	return item, nil
}
