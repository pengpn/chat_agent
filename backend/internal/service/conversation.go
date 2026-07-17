package service

import (
	"context"
	"errors"
	"strings"

	"github.com/example/agent-platform/backend/internal/database/model"
	"github.com/example/agent-platform/backend/internal/domain"
	"github.com/example/agent-platform/backend/internal/repository"
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

func (s *ConversationService) Create(ctx context.Context, userID, title string) (model.Conversation, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "New conversation"
	}
	conversation := model.Conversation{UserID: userID, Title: truncate(title, 120)}
	if err := s.conversations.Create(ctx, &conversation); err != nil {
		return model.Conversation{}, err
	}
	return conversation, nil
}

func (s *ConversationService) ListMessages(ctx context.Context, userID, conversationID string) ([]model.Message, error) {
	return s.messages.ListByConversationForUser(ctx, userID, conversationID)
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
