package service

import (
	"context"
	"errors"
	"strings"

	"github.com/example/agent-platform/backend/internal/database/model"
	"github.com/example/agent-platform/backend/internal/domain"
	"github.com/example/agent-platform/backend/internal/pagination"
	"github.com/example/agent-platform/backend/internal/repository"
)

const (
	DefaultConversationPageSize = 30
	DefaultMessagePageSize      = 50
	MaxPageSize                 = 100
)

type ConversationService struct {
	conversations repository.ConversationRepository
	messages      repository.MessageRepository
}

func NewConversation(conversations repository.ConversationRepository, messages repository.MessageRepository) *ConversationService {
	return &ConversationService{conversations: conversations, messages: messages}
}

func (s *ConversationService) List(ctx context.Context, userID string) ([]model.Conversation, error) {
	return s.conversations.ListByUserID(ctx, userID)
}

func (s *ConversationService) ListPage(ctx context.Context, userID, rawCursor, search string, limit int) (pagination.Page[model.Conversation], error) {
	cursor, err := decodeCursor(rawCursor)
	if err != nil {
		return pagination.Page[model.Conversation]{}, err
	}
	limit, err = normalizePageLimit(limit, DefaultConversationPageSize)
	if err != nil {
		return pagination.Page[model.Conversation]{}, err
	}
	items, err := s.conversations.ListPageByUserID(ctx, userID, repository.ConversationPageQuery{
		Cursor: cursor, Limit: limit, Search: truncate(strings.TrimSpace(search), 200),
	})
	if err != nil {
		return pagination.Page[model.Conversation]{}, err
	}
	return pagination.BuildPage(items, limit, func(item model.Conversation) pagination.Cursor {
		return pagination.Cursor{Time: item.UpdatedAt, ID: item.ID}
	}), nil
}

func (s *ConversationService) Create(ctx context.Context, userID, title string) (model.Conversation, error) {
	title = strings.TrimSpace(title)
	titleStatus := "manual"
	if title == "" {
		title = "New conversation"
		titleStatus = "default"
	}
	conversation := model.Conversation{UserID: userID, Title: truncate(title, 120), TitleStatus: titleStatus}
	if err := s.conversations.Create(ctx, &conversation); err != nil {
		return model.Conversation{}, err
	}
	return conversation, nil
}

func (s *ConversationService) ListMessages(ctx context.Context, userID, conversationID string) ([]model.Message, error) {
	return s.messages.ListByConversationForUser(ctx, userID, conversationID)
}

func (s *ConversationService) ListMessagesPage(ctx context.Context, userID, conversationID, rawCursor string, limit int) (pagination.Page[model.Message], error) {
	cursor, err := decodeCursor(rawCursor)
	if err != nil {
		return pagination.Page[model.Message]{}, err
	}
	limit, err = normalizePageLimit(limit, DefaultMessagePageSize)
	if err != nil {
		return pagination.Page[model.Message]{}, err
	}
	items, err := s.messages.ListPageByConversationForUser(ctx, userID, conversationID, cursor, limit)
	if err != nil {
		return pagination.Page[model.Message]{}, err
	}
	return pagination.BuildPage(items, limit, func(item model.Message) pagination.Cursor {
		return pagination.Cursor{Time: item.CreatedAt, ID: item.ID}
	}), nil
}

func decodeCursor(raw string) (*pagination.Cursor, error) {
	cursor, err := pagination.Decode(raw)
	if err != nil {
		return nil, ErrInvalidCursor
	}
	return cursor, nil
}

func normalizePageLimit(limit, defaultLimit int) (int, error) {
	if limit == 0 {
		return defaultLimit, nil
	}
	if limit < 1 || limit > MaxPageSize {
		return 0, ErrInvalidPageLimit
	}
	return limit, nil
}

func (s *ConversationService) ActivateBranch(ctx context.Context, userID, conversationID, messageID string) (model.Message, error) {
	message, err := s.messages.ActivateBranch(ctx, userID, conversationID, messageID)
	if errors.Is(err, domain.ErrNotFound) {
		return model.Message{}, ErrMessageNotFound
	}
	return message, err
}

func (s *ConversationService) UpdateTitle(ctx context.Context, userID, conversationID, title string) (model.Conversation, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return model.Conversation{}, ErrEmptyConversationTitle
	}
	conversation, err := s.conversations.UpdateTitle(ctx, userID, conversationID, truncate(title, 120))
	if errors.Is(err, domain.ErrNotFound) {
		return model.Conversation{}, ErrConversationNotFound
	}
	return conversation, err
}

func (s *ConversationService) Delete(ctx context.Context, userID, conversationID string) error {
	err := s.conversations.Delete(ctx, userID, conversationID)
	if errors.Is(err, domain.ErrNotFound) {
		return ErrConversationNotFound
	}
	return err
}

func truncate(value string, max int) string {
	if len([]rune(value)) <= max {
		return value
	}
	return string([]rune(value)[:max])
}
