package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/example/agent-platform/backend/internal/database/model"
	"github.com/example/agent-platform/backend/internal/domain"
	"github.com/example/agent-platform/backend/internal/logger"
	"github.com/example/agent-platform/backend/internal/provider"
	"github.com/example/agent-platform/backend/internal/repository"
	"github.com/example/agent-platform/backend/internal/tool"
)

type ChatService struct {
	repository   repository.ChatRepository
	providers    provider.Resolver
	tools        *tool.Registry
	maxToolCalls int
	thinking     *provider.ThinkingConfig
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

func NewChat(repository repository.ChatRepository, providers provider.Resolver, tools *tool.Registry, maxToolCalls int, thinking *provider.ThinkingConfig, systemPrompt string) *ChatService {
	return &ChatService{repository: repository, providers: providers, tools: tools, maxToolCalls: maxToolCalls, thinking: thinking, systemPrompt: systemPrompt}
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
	request := provider.ChatRequest{Model: input.Model, Messages: requestMessages, Temperature: input.Temperature}
	if s.providers.SupportsTools(input.Provider) && s.tools != nil {
		request.Tools = s.tools.Definitions()
	}
	if s.thinking != nil && s.thinking.Enabled && s.providers.SupportsThinking(input.Provider, input.Model) {
		thinking := *s.thinking
		request.Thinking = &thinking
	}
	session.request = request
	return session, nil
}

func (s *ChatService) Stream(ctx context.Context, session *ChatSession, onEvent func(provider.StreamEvent)) (model.Message, string, error) {
	var answer strings.Builder
	var think strings.Builder
	var usage provider.TokenUsage
	usageReported := false
	status := "complete"
	messages := append([]provider.Message(nil), session.request.Messages...)
	toolCalls := 0

	for {
		request := session.request
		request.Messages = messages
		var turnAnswer, turnThink strings.Builder
		var turnUsage provider.TokenUsage
		turnUsageReported := false
		requestedTools := make([]provider.ToolCall, 0)

		for event := range session.provider.ChatStream(ctx, request) {
			if event.Err != nil {
				status = "error"
				onEvent(event)
				break
			}
			if event.Delta != "" {
				turnAnswer.WriteString(event.Delta)
				answer.WriteString(event.Delta)
				onEvent(event)
			}
			if event.ThinkDelta != "" {
				turnThink.WriteString(event.ThinkDelta)
				think.WriteString(event.ThinkDelta)
				onEvent(event)
			}
			if event.Usage != nil {
				turnUsage.Merge(*event.Usage)
				turnUsageReported = true
			}
			if len(event.ToolCalls) > 0 {
				requestedTools = append(requestedTools, event.ToolCalls...)
			}
			if event.Done {
				break
			}
		}
		if turnUsageReported {
			usage.Add(turnUsage)
			usageReported = true
		}
		if status != "complete" || ctx.Err() != nil || len(requestedTools) == 0 {
			break
		}
		if s.tools == nil {
			status = "error"
			onEvent(provider.StreamEvent{Err: errors.New("模型请求了工具，但服务端未启用工具注册表")})
			break
		}
		if toolCalls >= s.maxToolCalls {
			status = "error"
			onEvent(provider.StreamEvent{Err: fmt.Errorf("工具调用超过最大轮次 %d", s.maxToolCalls)})
			break
		}

		toolCalls++
		messages = s.executeToolCalls(ctx, messages, turnAnswer.String(), turnThink.String(), requestedTools)
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
	completed, err := s.repository.Finalize(context.Background(), session.AssistantMessage.ID, answer.String(), think.String(), status, usageData)
	return completed, status, err
}

// Chat runs a non-streaming request, including the same server-side tool loop
// as Stream, and returns content plus model reasoning as one complete result.
func (s *ChatService) Chat(ctx context.Context, session *ChatSession) (model.Message, error) {
	var answer, think strings.Builder
	var usage provider.TokenUsage
	usageReported := false
	status := "complete"
	var chatErr error
	messages := append([]provider.Message(nil), session.request.Messages...)
	toolCalls := 0

	for {
		request := session.request
		request.Messages = messages
		result, err := session.provider.Chat(ctx, request)
		if err != nil {
			status = "error"
			chatErr = err
			break
		}
		answer.WriteString(result.Content)
		think.WriteString(result.Think)
		if result.Usage != nil {
			usage.Add(*result.Usage)
			usageReported = true
		}
		if ctx.Err() != nil || len(result.ToolCalls) == 0 {
			break
		}
		if s.tools == nil {
			status = "error"
			chatErr = errors.New("模型请求了工具，但服务端未启用工具注册表")
			break
		}
		if toolCalls >= s.maxToolCalls {
			status = "error"
			chatErr = fmt.Errorf("工具调用超过最大轮次 %d", s.maxToolCalls)
			break
		}
		toolCalls++
		messages = s.executeToolCalls(ctx, messages, result.Content, result.Think, result.ToolCalls)
	}
	if ctx.Err() != nil {
		status = "stopped"
		if chatErr == nil {
			chatErr = ctx.Err()
		}
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
	completed, finalizeErr := s.repository.Finalize(context.Background(), session.AssistantMessage.ID, answer.String(), think.String(), status, usageData)
	if finalizeErr != nil {
		return model.Message{}, finalizeErr
	}
	if chatErr != nil {
		return completed, chatErr
	}
	return completed, nil
}

func (s *ChatService) executeToolCalls(ctx context.Context, messages []provider.Message, assistantContent, assistantThink string, calls []provider.ToolCall) []provider.Message {
	messages = append(messages, provider.Message{Role: "assistant", Content: assistantContent, ReasoningContent: assistantThink, ToolCalls: calls})
	for _, call := range calls {
		logger.Info(ctx, "tool execution started",
			logger.String("tool_call_id", call.ID),
			logger.String("tool_name", call.Function.Name),
		)
		content, err := s.tools.Execute(ctx, call)
		if err != nil {
			logger.Warn(ctx, "tool execution failed",
				logger.String("tool_call_id", call.ID),
				logger.String("tool_name", call.Function.Name),
				logger.Err(err),
			)
			content = toolErrorResult(err)
		} else {
			logger.Info(ctx, "tool execution completed",
				logger.String("tool_call_id", call.ID),
				logger.String("tool_name", call.Function.Name),
			)
		}
		messages = append(messages, provider.Message{Role: "tool", ToolCallID: call.ID, Content: content})
	}
	return messages
}

func toolErrorResult(err error) string {
	payload, _ := json.Marshal(struct {
		Error string `json:"error"`
	}{Error: err.Error()})
	return string(payload)
}
