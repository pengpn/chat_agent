// Package repository contains all GORM queries and persistence operations.
package repository

import (
	"context"

	"github.com/example/agent-platform/backend/internal/database/model"
	"gorm.io/gorm"
)

// Repositories groups model-specific repositories and provides transaction
// boundaries for services that coordinate multiple persistence operations.
type Repositories struct {
	db            *gorm.DB
	Users         UserRepository
	Conversations ConversationRepository
	Messages      MessageRepository
}

// ChatRepository groups the persistence operations required for a conversation
// turn, whether it uses Chat or ChatStream.
type ChatRepository interface {
	FindByIDForUser(ctx context.Context, userID, id string) (model.Conversation, error)
	ListByConversationForUser(ctx context.Context, userID, conversationID string) ([]model.Message, error)
	CreateTurn(ctx context.Context, userMessage, assistantMessage *model.Message) error
	Finalize(ctx context.Context, id, content, think, status string, usage model.MessageUsage) (model.Message, error)
}

func New(db *gorm.DB) *Repositories {
	return &Repositories{
		db:            db,
		Users:         newUserRepository(db),
		Conversations: newConversationRepository(db),
		Messages:      newMessageRepository(db),
	}
}

func (r *Repositories) transaction(ctx context.Context, fn func(tx *Repositories) error) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(New(tx))
	})
}

// CreateTurn stores the user and assistant messages together, then advances
// the conversation timestamp. It is kept atomic so service code does not need
// to understand GORM transactions.
func (r *Repositories) CreateTurn(ctx context.Context, userMessage, assistantMessage *model.Message) error {
	return r.transaction(ctx, func(tx *Repositories) error {
		if err := tx.Messages.Create(ctx, userMessage); err != nil {
			return err
		}
		if err := tx.Messages.Create(ctx, assistantMessage); err != nil {
			return err
		}
		return tx.Conversations.Touch(ctx, userMessage.ConversationID)
	})
}

func (r *Repositories) FindByIDForUser(ctx context.Context, userID, id string) (model.Conversation, error) {
	return r.Conversations.FindByIDForUser(ctx, userID, id)
}

func (r *Repositories) ListByConversationForUser(ctx context.Context, userID, conversationID string) ([]model.Message, error) {
	return r.Messages.ListByConversationForUser(ctx, userID, conversationID)
}

func (r *Repositories) Finalize(ctx context.Context, id, content, think, status string, usage model.MessageUsage) (model.Message, error) {
	return r.Messages.Finalize(ctx, id, content, think, status, usage)
}

func Migrate(ctx context.Context, db *gorm.DB) error {
	if err := db.WithContext(ctx).Exec("CREATE EXTENSION IF NOT EXISTS pgcrypto").Error; err != nil {
		return err
	}
	return db.WithContext(ctx).AutoMigrate(
		&model.User{},
		&model.Conversation{},
		&model.Message{},
	)
}
