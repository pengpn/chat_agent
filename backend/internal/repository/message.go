package repository

import (
	"context"
	"errors"

	"github.com/example/agent-platform/backend/internal/database/model"
	"github.com/example/agent-platform/backend/internal/domain"
	"github.com/example/agent-platform/backend/internal/pagination"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// MessageRepository defines the message persistence operations used by
// services.
type MessageRepository interface {
	ListByConversationForUser(ctx context.Context, userID, conversationID string) ([]model.Message, error)
	ListPageByConversationForUser(ctx context.Context, userID, conversationID string, cursor *pagination.Cursor, limit int) ([]model.Message, error)
	FindByIDForUser(ctx context.Context, userID, conversationID, messageID string) (model.Message, error)
	Create(ctx context.Context, message *model.Message) error
	Finalize(ctx context.Context, id string, completion model.MessageCompletion) (model.Message, error)
	CreateRegeneration(ctx context.Context, userID, targetMessageID, replyToMessageID string, assistant *model.Message) error
	CreateEditedTurn(ctx context.Context, userID, targetMessageID string, userMessage, assistantMessage *model.Message) error
	ActivateBranch(ctx context.Context, userID, conversationID, messageID string) (model.Message, error)
}

// GormMessageRepository is the PostgreSQL/GORM implementation of
// MessageRepository.
type GormMessageRepository struct{ db *gorm.DB }

func newMessageRepository(db *gorm.DB) *GormMessageRepository { return &GormMessageRepository{db: db} }

func (r *GormMessageRepository) ListByConversationForUser(ctx context.Context, userID, conversationID string) ([]model.Message, error) {
	items := make([]model.Message, 0)
	err := r.db.WithContext(ctx).
		Preload("Attachments").
		Preload("Citations", func(db *gorm.DB) *gorm.DB { return db.Order("ordinal ASC") }).
		Model(&model.Message{}).
		Select("messages.*").
		Joins("JOIN conversations ON conversations.id = messages.conversation_id").
		Where("messages.conversation_id = ? AND conversations.user_id = ?", conversationID, userID).
		Order("messages.created_at ASC").
		Find(&items).Error
	return items, err
}

// ListPageByConversationForUser queries newest-first with a stable
// (created_at, id) cursor. The user constraint is part of the same SQL query so
// another user's conversation can never be paged by guessing its UUID.
func (r *GormMessageRepository) ListPageByConversationForUser(ctx context.Context, userID, conversationID string, cursor *pagination.Cursor, limit int) ([]model.Message, error) {
	items := make([]model.Message, 0, limit+1)
	query := r.db.WithContext(ctx).
		Preload("Attachments").
		Preload("Citations", func(db *gorm.DB) *gorm.DB { return db.Order("ordinal ASC") }).
		Model(&model.Message{}).
		Select("messages.*").
		Joins("JOIN conversations ON conversations.id = messages.conversation_id").
		Where("messages.conversation_id = ? AND conversations.user_id = ?", conversationID, userID)
	if cursor != nil {
		query = query.Where("(messages.created_at, messages.id) < (?, ?)", cursor.Time, cursor.ID)
	}
	err := query.Order("messages.created_at DESC, messages.id DESC").Limit(limit + 1).Find(&items).Error
	return items, err
}

func (r *GormMessageRepository) FindByIDForUser(ctx context.Context, userID, conversationID, messageID string) (model.Message, error) {
	var item model.Message
	err := r.db.WithContext(ctx).
		Preload("Attachments").
		Preload("Citations", func(db *gorm.DB) *gorm.DB { return db.Order("ordinal ASC") }).
		Model(&model.Message{}).
		Select("messages.*").
		Joins("JOIN conversations ON conversations.id = messages.conversation_id").
		Where("messages.id = ? AND messages.conversation_id = ? AND conversations.user_id = ?", messageID, conversationID, userID).
		First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.Message{}, domain.ErrNotFound
	}
	return item, err
}

func (r *GormMessageRepository) Create(ctx context.Context, message *model.Message) error {
	return r.db.WithContext(ctx).Create(message).Error
}

func (r *GormMessageRepository) Finalize(ctx context.Context, id string, completion model.MessageCompletion) (model.Message, error) {
	var item model.Message
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&item, "id = ?", id).Error; err != nil {
			return err
		}
		updates := map[string]any{
			"content": completion.Content, "think": completion.Think,
			"structured_output": completion.StructuredOutput, "status": completion.Status,
			"finish_reason": completion.FinishReason,
			"input_tokens":  completion.Usage.InputTokens, "output_tokens": completion.Usage.OutputTokens,
			"total_tokens": completion.Usage.TotalTokens, "usage_reported": completion.Usage.UsageReported,
			"cost_nano_usd": completion.Usage.CostNanoUSD, "cost_known": completion.Usage.CostKnown,
		}
		if err := tx.Model(&item).Updates(updates).Error; err != nil {
			return err
		}
		if err := tx.Where("message_id = ?", id).Delete(&model.MessageCitation{}).Error; err != nil {
			return err
		}
		if len(completion.Citations) > 0 {
			citations := append([]model.MessageCitation(nil), completion.Citations...)
			var ownerID string
			if err := tx.Model(&model.Conversation{}).Select("user_id").Where("id = ?", item.ConversationID).Scan(&ownerID).Error; err != nil {
				return err
			}
			for index := range citations {
				citations[index].ID = ""
				citations[index].MessageID = id
				owned, err := citationReferencesOwned(tx, ownerID, citations[index])
				if err != nil {
					return err
				}
				if !owned {
					// 模型还在输出时，来源文档可能已被删除。
					// 已经校验过的引用快照仍然保留，但不能把已消失或属于其他用户的实时引用写入数据库。
					citations[index].KnowledgeBaseID = nil
					citations[index].DocumentID = nil
					citations[index].ChunkID = nil
				}
			}
			if err := tx.Create(&citations).Error; err != nil {
				return err
			}
		}
		return tx.Preload("Citations", func(db *gorm.DB) *gorm.DB { return db.Order("ordinal ASC") }).First(&item, "id = ?", id).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.Message{}, domain.ErrNotFound
	}
	return item, err
}

func citationReferencesOwned(tx *gorm.DB, userID string, citation model.MessageCitation) (bool, error) {
	if userID == "" || citation.KnowledgeBaseID == nil || citation.DocumentID == nil || citation.ChunkID == nil {
		return false, nil
	}
	var count int64
	err := tx.Table("knowledge_chunks").
		Joins("JOIN knowledge_documents ON knowledge_documents.id = knowledge_chunks.document_id").
		Joins("JOIN knowledge_bases ON knowledge_bases.id = knowledge_documents.knowledge_base_id").
		Where("knowledge_chunks.id = ? AND knowledge_documents.id = ? AND knowledge_bases.id = ? AND knowledge_bases.user_id = ?",
			*citation.ChunkID, *citation.DocumentID, *citation.KnowledgeBaseID, userID).
		Count(&count).Error
	return count == 1, err
}

// CreateRegeneration atomically deactivates the previous answer versions and
// creates a new streaming version. The conversation lock serializes concurrent
// regenerate requests for the same answer group.
func (r *GormMessageRepository) CreateRegeneration(ctx context.Context, userID, targetMessageID, replyToMessageID string, assistant *model.Message) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var conversation model.Conversation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND user_id = ?", assistant.ConversationID, userID).First(&conversation).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrNotFound
			}
			return err
		}
		if assistant.GenerationKey != nil {
			var count int64
			if err := tx.Model(&model.Message{}).Where("generation_key = ?", *assistant.GenerationKey).Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				return domain.ErrAlreadyExists
			}
		}
		var target model.Message
		err := tx.Model(&model.Message{}).
			Select("messages.*").
			Joins("JOIN conversations ON conversations.id = messages.conversation_id").
			Where("messages.id = ? AND messages.conversation_id = ? AND conversations.user_id = ?", targetMessageID, assistant.ConversationID, userID).
			First(&target).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.ErrNotFound
		}
		if err != nil {
			return err
		}
		if err := activateMessagePath(tx, target); err != nil {
			return err
		}
		if target.ParentMessageID == nil || target.ReplyToMessageID == nil {
			if err := tx.Model(&model.Message{}).Where("id = ?", target.ID).Updates(map[string]any{
				"parent_message_id":   replyToMessageID,
				"reply_to_message_id": replyToMessageID,
				"response_version":    1,
			}).Error; err != nil {
				return err
			}
		}
		var latest int64
		if err := tx.Model(&model.Message{}).
			Where("conversation_id = ? AND role = 'assistant' AND reply_to_message_id = ?", target.ConversationID, replyToMessageID).
			Select("COALESCE(MAX(response_version), 0)").Scan(&latest).Error; err != nil {
			return err
		}
		if latest == 0 {
			latest = 1
		}
		if err := tx.Model(&model.Message{}).
			Where("conversation_id = ? AND role = 'assistant' AND reply_to_message_id = ?", target.ConversationID, replyToMessageID).
			Updates(map[string]any{"is_active_response": false, "is_active_child": false}).Error; err != nil {
			return err
		}
		assistant.ConversationID = target.ConversationID
		assistant.ParentMessageID = &replyToMessageID
		assistant.ReplyToMessageID = &replyToMessageID
		assistant.ResponseVersion = latest + 1
		assistant.IsActiveResponse = true
		assistant.IsActiveChild = true
		assistant.RegeneratedFrom = &targetMessageID
		if err := tx.Create(assistant).Error; err != nil {
			return err
		}
		return tx.Model(&model.Conversation{}).Where("id = ?", target.ConversationID).Updates(map[string]any{
			"updated_at":                 gorm.Expr("CURRENT_TIMESTAMP"),
			"summary":                    "",
			"summary_through_message_id": nil,
			"summary_updated_at":         nil,
		}).Error
	})
}

func (r *GormMessageRepository) CreateEditedTurn(ctx context.Context, userID, targetMessageID string, userMessage, assistantMessage *model.Message) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var conversation model.Conversation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND user_id = ?", userMessage.ConversationID, userID).First(&conversation).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrNotFound
			}
			return err
		}
		if assistantMessage.GenerationKey != nil {
			var count int64
			if err := tx.Model(&model.Message{}).Where("generation_key = ?", *assistantMessage.GenerationKey).Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				return domain.ErrAlreadyExists
			}
		}
		var target model.Message
		if err := tx.Where("id = ? AND conversation_id = ? AND role = 'user'", targetMessageID, userMessage.ConversationID).First(&target).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrNotFound
			}
			return err
		}
		if err := activateMessagePath(tx, target); err != nil {
			return err
		}
		if err := siblingQuery(tx, target.ConversationID, "user", target.ParentMessageID).
			Update("is_active_child", false).Error; err != nil {
			return err
		}
		userMessage.ParentMessageID = target.ParentMessageID
		userMessage.IsActiveChild = true
		userMessage.EditedFrom = &targetMessageID
		if err := tx.Create(userMessage).Error; err != nil {
			return err
		}
		assistantMessage.ConversationID = target.ConversationID
		assistantMessage.ParentMessageID = &userMessage.ID
		assistantMessage.ReplyToMessageID = &userMessage.ID
		assistantMessage.ResponseVersion = 1
		assistantMessage.IsActiveResponse = true
		assistantMessage.IsActiveChild = true
		if err := tx.Create(assistantMessage).Error; err != nil {
			return err
		}
		return invalidateConversationSummary(tx, target.ConversationID, true)
	})
}

func (r *GormMessageRepository) ActivateBranch(ctx context.Context, userID, conversationID, messageID string) (model.Message, error) {
	var selected model.Message
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var conversation model.Conversation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND user_id = ?", conversationID, userID).First(&conversation).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrNotFound
			}
			return err
		}
		err := tx.Where("id = ? AND conversation_id = ?", messageID, conversationID).First(&selected).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.ErrNotFound
		}
		if err != nil {
			return err
		}
		if err := activateMessagePath(tx, selected); err != nil {
			return err
		}
		return invalidateConversationSummary(tx, conversationID, false)
	})
	if err != nil {
		return model.Message{}, err
	}
	selected.IsActiveChild = true
	if selected.Role == "assistant" {
		selected.IsActiveResponse = true
	}
	return selected, nil
}

// activateMessagePath selects a message and every ancestor leading to it.
// This makes branch activation robust even when a client opens a deep node
// from an inactive historical branch instead of navigating one level at a time.
func activateMessagePath(tx *gorm.DB, selected model.Message) error {
	current := selected
	for {
		deactivate := map[string]any{"is_active_child": false}
		if current.Role == "assistant" {
			deactivate["is_active_response"] = false
		}
		if err := siblingQuery(tx, current.ConversationID, current.Role, current.ParentMessageID).
			Updates(deactivate).Error; err != nil {
			return err
		}
		activate := map[string]any{"is_active_child": true}
		if current.Role == "assistant" {
			activate["is_active_response"] = true
		}
		if err := tx.Model(&model.Message{}).Where("id = ?", current.ID).Updates(activate).Error; err != nil {
			return err
		}
		if current.ParentMessageID == nil {
			return nil
		}
		var parent model.Message
		if err := tx.Where("id = ? AND conversation_id = ?", *current.ParentMessageID, current.ConversationID).
			First(&parent).Error; err != nil {
			return err
		}
		current = parent
	}
}

func siblingQuery(tx *gorm.DB, conversationID, role string, parentID *string) *gorm.DB {
	query := tx.Model(&model.Message{}).Where("conversation_id = ? AND role = ?", conversationID, role)
	if parentID == nil {
		return query.Where("parent_message_id IS NULL")
	}
	return query.Where("parent_message_id = ?", *parentID)
}

func invalidateConversationSummary(tx *gorm.DB, conversationID string, touch bool) error {
	updates := map[string]any{
		"summary":                    "",
		"summary_through_message_id": nil,
		"summary_updated_at":         nil,
	}
	if touch {
		updates["updated_at"] = gorm.Expr("CURRENT_TIMESTAMP")
	}
	return tx.Model(&model.Conversation{}).Where("id = ?", conversationID).Updates(updates).Error
}
