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
	ClaimTitleGeneration(ctx context.Context, userID, id string, staleBefore time.Time) (TitleGenerationClaim, bool, error)
	CompleteTitleGeneration(ctx context.Context, userID, id, title, status string, usage TitleGenerationUsage) (model.Conversation, bool, error)
	Delete(ctx context.Context, userID, id string) error
	Touch(ctx context.Context, id string) error
	SaveSummaryVersion(ctx context.Context, userID string, version *model.ConversationSummaryVersion, activate bool) error
}

type TitleGenerationClaim struct {
	Conversation model.Conversation
	UserMessage  model.Message
	Answer       model.Message
}

type TitleGenerationUsage struct {
	Provider      string
	Model         string
	InputTokens   int64
	OutputTokens  int64
	TotalTokens   int64
	UsageReported bool
	CostNanoUSD   int64
	CostKnown     bool
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
	result := r.db.WithContext(ctx).Model(&model.Conversation{}).Where("id = ? AND user_id = ?", id, userID).
		Updates(map[string]any{"title": title, "title_status": "manual", "title_generation_started": nil})
	if result.Error != nil {
		return model.Conversation{}, result.Error
	}
	if result.RowsAffected == 0 {
		return model.Conversation{}, domain.ErrNotFound
	}
	return r.FindByIDForUser(ctx, userID, id)
}

func (r *GormConversationRepository) ClaimTitleGeneration(ctx context.Context, userID, id string, staleBefore time.Time) (TitleGenerationClaim, bool, error) {
	var claim TitleGenerationClaim
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND user_id = ?", id, userID).First(&claim.Conversation).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrNotFound
			}
			return err
		}
		status := claim.Conversation.TitleStatus
		if status != "default" && !(status == "generating" && claim.Conversation.TitleGenerationStarted != nil && claim.Conversation.TitleGenerationStarted.Before(staleBefore)) {
			return nil
		}
		if err := tx.Where("conversation_id = ? AND role = 'assistant' AND status = 'complete'", id).
			Order("created_at ASC").First(&claim.Answer).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		userQuery := tx.Where("conversation_id = ? AND role = 'user'", id)
		if claim.Answer.ReplyToMessageID != nil {
			userQuery = userQuery.Where("id = ?", *claim.Answer.ReplyToMessageID)
		} else {
			userQuery = userQuery.Order("created_at ASC")
		}
		if err := userQuery.First(&claim.UserMessage).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		now := time.Now()
		if err := tx.Model(&model.Conversation{}).Where("id = ?", id).Updates(map[string]any{
			"title_status": "generating", "title_generation_started": now,
		}).Error; err != nil {
			return err
		}
		claim.Conversation.TitleStatus = "generating"
		claim.Conversation.TitleGenerationStarted = &now
		return nil
	})
	if err != nil {
		return TitleGenerationClaim{}, false, err
	}
	return claim, claim.Conversation.TitleStatus == "generating" && claim.UserMessage.ID != "" && claim.Answer.ID != "", nil
}

func (r *GormConversationRepository) CompleteTitleGeneration(ctx context.Context, userID, id, title, status string, usage TitleGenerationUsage) (model.Conversation, bool, error) {
	var conversation model.Conversation
	completed := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ?", id, userID).First(&conversation).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrNotFound
			}
			return err
		}
		if conversation.TitleStatus != "generating" {
			return nil
		}
		now := time.Now()
		updates := map[string]any{
			"title": title, "title_status": status, "title_provider": usage.Provider, "title_model": usage.Model,
			"title_input_tokens": usage.InputTokens, "title_output_tokens": usage.OutputTokens,
			"title_total_tokens": usage.TotalTokens, "title_usage_reported": usage.UsageReported,
			"title_cost_nano_usd": usage.CostNanoUSD, "title_cost_known": usage.CostKnown,
			"title_generation_started": nil, "title_generated_at": now,
		}
		if err := tx.Model(&model.Conversation{}).Where("id = ?", id).Updates(updates).Error; err != nil {
			return err
		}
		completed = true
		return tx.Where("id = ?", id).First(&conversation).Error
	})
	return conversation, completed, err
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
