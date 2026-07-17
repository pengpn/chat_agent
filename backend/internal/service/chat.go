package service

import (
	"context"
	"errors"
	"strings"

	"github.com/example/agent-platform/backend/internal/database/model"
	"github.com/example/agent-platform/backend/internal/domain"
	"github.com/example/agent-platform/backend/internal/provider"
	"github.com/example/agent-platform/backend/internal/repository"
)

type ChatService struct {
	repository   repository.ChatRepository
	providers    *provider.Registry
	systemPrompt string
}

type ChatInput struct {
	Content     string
	Provider    string
	Model       string
	Temperature *float64
}

type ChatSession struct {
	UserMessage      model.Message
	AssistantMessage model.Message
	provider         provider.Provider
	request          provider.ChatRequest
}

func NewChat(repository repository.ChatRepository, providers *provider.Registry, systemPrompt string) *ChatService {
	return &ChatService{repository: repository, providers: providers, systemPrompt: systemPrompt}
}

func (s *ChatService) Prepare(ctx context.Context, userID, conversationID string, input ChatInput) (*ChatSession, error) {
	input.Content = strings.TrimSpace(input.Content)
	if input.Content == "" || len([]rune(input.Content)) > 100_000 {
		return nil, ErrInvalidMessageLength
	}
	if input.Temperature != nil && (*input.Temperature < 0 || *input.Temperature > 2) {
		return nil, ErrInvalidTemperature
	}
	if _, err := s.repository.FindByIDForUser(ctx, userID, conversationID); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, ErrConversationNotFound
		}
		return nil, err
	}
	modelProvider, err := s.providers.Get(input.Provider, input.Model)
	if err != nil {
		return nil, &ProviderSelectionError{err: err}
	}
	history, err := s.repository.ListByConversationForUser(ctx, userID, conversationID)
	if err != nil {
		return nil, err
	}

	session := &ChatSession{
		UserMessage: model.Message{
			ConversationID: conversationID,
			Role:           "user",
			Content:        input.Content,
			Status:         "complete",
		},
		AssistantMessage: model.Message{
			ConversationID: conversationID,
			Role:           "assistant",
			Provider:       input.Provider,
			Model:          input.Model,
			Status:         "streaming",
		},
	}
	if err := s.repository.CreateTurn(ctx, &session.UserMessage, &session.AssistantMessage); err != nil {
		return nil, err
	}

	requestMessages := make([]provider.Message, 0, len(history)+2)
	if system := strings.TrimSpace(s.systemPrompt); system != "" {
		requestMessages = append(requestMessages, provider.Message{Role: "system", Content: system})
	}
	for _, message := range history {
		if message.Status == "complete" || message.Status == "stopped" {
			requestMessages = append(requestMessages, provider.Message{Role: message.Role, Content: message.Content})
		}
	}
	requestMessages = append(requestMessages, provider.Message{Role: "user", Content: session.UserMessage.Content})
	session.provider = modelProvider
	session.request = provider.ChatRequest{Model: input.Model, Messages: requestMessages, Temperature: input.Temperature}
	return session, nil
}

func (s *ChatService) Stream(ctx context.Context, session *ChatSession, onEvent func(provider.StreamEvent)) (model.Message, string, error) {
	var answer strings.Builder
	var usage provider.TokenUsage
	usageReported := false
	status := "complete"
	for event := range session.provider.ChatStream(ctx, session.request) {
		if event.Err != nil {
			status = "error"
			onEvent(event)
			break
		}
		if event.Delta != "" {
			answer.WriteString(event.Delta)
			onEvent(event)
		}
		if event.Usage != nil {
			usage.Merge(*event.Usage)
			usageReported = true
		}
		if event.Done {
			break
		}
	}
	if ctx.Err() != nil {
		status = "stopped"
	}
	usageData := model.MessageUsage{
		InputTokens:   usage.InputTokens,
		OutputTokens:  usage.OutputTokens,
		TotalTokens:   usage.TotalTokens,
		UsageReported: usageReported,
	}
	if usageReported {
		usageData.CostNanoUSD, usageData.CostKnown = s.providers.CalculateCost(session.AssistantMessage.Provider, session.AssistantMessage.Model, usage)
	}
	completed, err := s.repository.Finalize(context.Background(), session.AssistantMessage.ID, answer.String(), status, usageData)
	return completed, status, err
}
