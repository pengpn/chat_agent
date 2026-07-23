package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/example/agent-platform/backend/internal/database/model"
	"github.com/example/agent-platform/backend/internal/domain"
	"github.com/example/agent-platform/backend/internal/llmgateway"
	"github.com/example/agent-platform/backend/internal/logger"
	"github.com/example/agent-platform/backend/internal/provider"
	"github.com/example/agent-platform/backend/internal/repository"
	"github.com/example/agent-platform/backend/internal/tool"
)

type ChatService struct {
	repository   repository.ChatRepository
	gateway      llmgateway.Gateway
	tools        *tool.Registry
	maxToolCalls int
	thinking     *provider.ThinkingConfig
	systemPrompt string
	stream       StreamConfig
}

type StreamConfig struct {
	TTFTTimeout     time.Duration
	TotalTimeout    time.Duration
	FinalizeTimeout time.Duration
}

type ChatInput struct {
	Content     string
	Provider    string
	Model       string
	Temperature *float64
	// IdempotencyKey comes from the HTTP Idempotency-Key header. It scopes one
	// complete business message rather than individual model retries.
	IdempotencyKey string
}

type ChatSession struct {
	UserMessage      model.Message
	AssistantMessage model.Message
	OperationID      string
	UserID           string
	request          provider.ChatRequest
}

func NewChat(repository repository.ChatRepository, gateway llmgateway.Gateway, tools *tool.Registry, maxToolCalls int, thinking *provider.ThinkingConfig, systemPrompt string, stream StreamConfig) *ChatService {
	return &ChatService{repository: repository, gateway: gateway, tools: tools, maxToolCalls: maxToolCalls, thinking: thinking, systemPrompt: systemPrompt, stream: stream}
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
	if err := s.gateway.Validate(input.Provider, input.Model); err != nil {
		return nil, &ProviderSelectionError{err: err}
	}
	history, err := s.repository.ListByConversationForUser(ctx, userID, conversationID)
	if err != nil {
		return nil, err
	}

	operationID := llmgateway.OperationID(userID, conversationID, strings.TrimSpace(input.IdempotencyKey))
	if err := s.gateway.Begin(ctx, operationID); err != nil {
		return nil, err
	}
	session := &ChatSession{
		OperationID: operationID,
		UserID:      userID,
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
		s.gateway.Abandon(operationID)
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
	request := provider.ChatRequest{Model: input.Model, Messages: requestMessages, Temperature: input.Temperature}
	if s.gateway.SupportsTools(input.Provider) && s.tools != nil {
		request.Tools = s.tools.Definitions()
	}
	if s.thinking != nil && s.thinking.Enabled && s.gateway.SupportsThinking(input.Provider, input.Model) {
		thinking := *s.thinking
		request.Thinking = &thinking
	}
	session.request = request
	return session, nil
}

// 业务编排层，负责多轮模型调用、工具执行、内容聚合、用量统计和数据库落库。
func (s *ChatService) Stream(ctx context.Context, session *ChatSession, onEvent func(provider.StreamEvent)) (model.Message, string, error) {
	var answer strings.Builder
	var think strings.Builder
	var usage provider.TokenUsage
	usageReported := false
	status := "complete"
	finishReason := ""
	messages := append([]provider.Message(nil), session.request.Messages...)
	toolCalls := 0
	modelTurn := 0
	streamCtx := ctx
	cancelStream := func() {}
	if s.stream.TotalTimeout > 0 {
		streamCtx, cancelStream = context.WithTimeout(ctx, s.stream.TotalTimeout)
	}
	defer cancelStream()

	for {
		modelTurn++
		request := session.request
		request.Messages = messages
		turn := s.consumeProviderStream(ctx, streamCtx, llmgateway.Request{
			OperationID: session.OperationID, UserID: session.UserID,
			Provider: session.AssistantMessage.Provider, Model: session.AssistantMessage.Model,
			Turn: modelTurn, ChatRequest: request,
		}, onEvent)
		answer.WriteString(turn.answer)
		think.WriteString(turn.think)
		if turn.usageReported {
			usage.Add(turn.usage)
			usageReported = true
		}
		finishReason = turn.finishReason
		if turn.err != nil {
			kind := provider.StreamErrorKindOf(turn.err)
			logger.Warn(ctx, "model stream interrupted",
				logger.String("provider", session.AssistantMessage.Provider),
				logger.String("model", session.AssistantMessage.Model),
				logger.String("reason", string(kind)),
				logger.Err(turn.err),
			)
			finishReason = string(kind)
			if kind == provider.StreamErrorCanceled {
				status = "stopped"
			} else if kind == provider.StreamErrorUpstream {
				status = "error"
				onEvent(provider.StreamEvent{Err: turn.err})
			} else {
				status = "interrupted"
				onEvent(provider.StreamEvent{Err: turn.err})
			}
			break
		}
		if len(turn.toolCalls) == 0 {
			break
		}
		if s.tools == nil {
			status = "error"
			finishReason = "tool_registry_unavailable"
			onEvent(provider.StreamEvent{Err: errors.New("模型请求了工具，但服务端未启用工具注册表")})
			break
		}
		if toolCalls >= s.maxToolCalls {
			status = "error"
			finishReason = "tool_iteration_limit"
			onEvent(provider.StreamEvent{Err: fmt.Errorf("工具调用超过最大轮次 %d", s.maxToolCalls)})
			break
		}

		toolCalls++
		// 执行本轮模型请求的工具：把 assistant（含 tool_calls）和各 tool 结果追加进 messages，
		// 供下一轮 ChatStream 继续对话。
		messages = s.executeToolCalls(streamCtx, messages, turn.answer, turn.think, turn.toolCalls)
	}
	if ctx.Err() != nil {
		status = "stopped"
		finishReason = string(provider.StreamErrorCanceled)
	} else if errors.Is(streamCtx.Err(), context.DeadlineExceeded) && status == "complete" {
		status = "interrupted"
		finishReason = string(provider.StreamErrorTotalTimeout)
	} else if status == "complete" && finishReason == "" {
		finishReason = "stop"
	}
	usageData := model.MessageUsage{
		InputTokens:   usage.InputTokens,
		OutputTokens:  usage.OutputTokens,
		TotalTokens:   usage.TotalTokens,
		UsageReported: usageReported,
	}
	if usageReported {
		usageData.CostNanoUSD, usageData.CostKnown = s.gateway.CalculateCost(session.AssistantMessage.Provider, session.AssistantMessage.Model, usage)
	}
	completed, err := s.finalize(session.AssistantMessage.ID, model.MessageCompletion{
		Content: answer.String(), Think: think.String(), Status: status, FinishReason: finishReason, Usage: usageData,
	})
	if err != nil {
		s.gateway.Abandon(session.OperationID)
	} else {
		s.gateway.Complete(session.OperationID)
	}
	return completed, status, err
}

type streamTurn struct {
	answer        string
	think         string
	toolCalls     []provider.ToolCall
	usage         provider.TokenUsage
	usageReported bool
	finishReason  string
	err           error
}

func (s *ChatService) consumeProviderStream(parentCtx, streamCtx context.Context, request llmgateway.Request, onEvent func(provider.StreamEvent)) streamTurn {
	callCtx, cancel := context.WithCancel(streamCtx)
	defer cancel()
	events := s.gateway.ChatStream(callCtx, request)
	var answer, think strings.Builder
	result := streamTurn{toolCalls: make([]provider.ToolCall, 0)}
	var ttftTimer *time.Timer
	var ttft <-chan time.Time
	firstEventReceived := false
	var connectedAt time.Time

	stopTTFT := func() {
		if ttftTimer != nil && !ttftTimer.Stop() {
			select {
			case <-ttftTimer.C:
			default:
			}
		}
		ttft = nil
	}
	defer stopTTFT()

	for {
		select {
		case <-parentCtx.Done():
			result.err = provider.NewStreamError(provider.StreamErrorCanceled, parentCtx.Err())
			result.answer, result.think = answer.String(), think.String()
			return result
		case <-streamCtx.Done():
			kind := provider.StreamErrorTotalTimeout
			if parentCtx.Err() != nil {
				kind = provider.StreamErrorCanceled
			}
			result.err = provider.NewStreamError(kind, streamCtx.Err())
			result.answer, result.think = answer.String(), think.String()
			return result
		case <-ttft:
			result.err = provider.NewStreamError(provider.StreamErrorTTFTTimeout, errors.New("等待模型首个事件超时"))
			result.answer, result.think = answer.String(), think.String()
			return result
		case event, ok := <-events:
			if !ok {
				switch {
				case parentCtx.Err() != nil:
					result.err = provider.NewStreamError(provider.StreamErrorCanceled, parentCtx.Err())
				case errors.Is(streamCtx.Err(), context.DeadlineExceeded):
					result.err = provider.NewStreamError(provider.StreamErrorTotalTimeout, streamCtx.Err())
				default:
					result.err = provider.NewStreamError(provider.StreamErrorDisconnected, errors.New("模型流未收到正常结束标记"))
				}
				result.answer, result.think = answer.String(), think.String()
				return result
			}
			if event.Connected && !firstEventReceived && s.stream.TTFTTimeout > 0 {
				connectedAt = time.Now()
				stopTTFT()
				ttftTimer = time.NewTimer(s.stream.TTFTTimeout)
				ttft = ttftTimer.C
			}
			if event.Activity && !firstEventReceived {
				firstEventReceived = true
				stopTTFT()
				if !connectedAt.IsZero() {
					logger.Info(parentCtx, "model stream first event received", logger.Int64("ttft_ms", time.Since(connectedAt).Milliseconds()))
				}
			}
			if event.Err != nil {
				result.err = provider.ClassifyStreamError(parentCtx, event.Err)
				result.answer, result.think = answer.String(), think.String()
				return result
			}
			if event.Delta != "" {
				answer.WriteString(event.Delta)
				onEvent(provider.StreamEvent{Delta: event.Delta, Sequence: event.Sequence})
			}
			if event.ThinkDelta != "" {
				think.WriteString(event.ThinkDelta)
				onEvent(provider.StreamEvent{ThinkDelta: event.ThinkDelta, Sequence: event.Sequence})
			}
			if event.Usage != nil {
				result.usage.Merge(*event.Usage)
				result.usageReported = true
			}
			if len(event.ToolCalls) > 0 {
				result.toolCalls = append(result.toolCalls, event.ToolCalls...)
			}
			if event.Done {
				result.answer, result.think = answer.String(), think.String()
				result.finishReason = event.FinishReason
				return result
			}
		}
	}
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
	modelTurn := 0

	for {
		modelTurn++
		request := session.request
		request.Messages = messages
		result, err := s.gateway.Chat(ctx, llmgateway.Request{
			OperationID: session.OperationID, UserID: session.UserID,
			Provider: session.AssistantMessage.Provider, Model: session.AssistantMessage.Model,
			Turn: modelTurn, ChatRequest: request,
		})
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
		usageData.CostNanoUSD, usageData.CostKnown = s.gateway.CalculateCost(session.AssistantMessage.Provider, session.AssistantMessage.Model, usage)
	}
	finishReason := "stop"
	if status != "complete" {
		finishReason = status
	}
	completed, finalizeErr := s.finalize(session.AssistantMessage.ID, model.MessageCompletion{
		Content: answer.String(), Think: think.String(), Status: status, FinishReason: finishReason, Usage: usageData,
	})
	if finalizeErr != nil {
		s.gateway.Abandon(session.OperationID)
		return model.Message{}, finalizeErr
	}
	s.gateway.Complete(session.OperationID)
	if chatErr != nil {
		return completed, chatErr
	}
	return completed, nil
}

// finalize is the only deliberately detached operation in the stream
// lifecycle. It has a strict deadline and only persists the terminal state so
// canceled requests do not leave messages stuck as "streaming".
func (s *ChatService) finalize(messageID string, completion model.MessageCompletion) (model.Message, error) {
	timeout := s.stream.FinalizeTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return s.repository.Finalize(ctx, messageID, completion)
}

func (s *ChatService) executeToolCalls(ctx context.Context, messages []provider.Message, assistantContent, assistantThink string, calls []provider.ToolCall) []provider.Message {
	messages = append(messages, provider.Message{Role: "assistant", Content: assistantContent, ReasoningContent: assistantThink, ToolCalls: calls})
	// 循环执行工具调用：把 tool 执行结果追加进 messages，供下一轮 ChatStream 继续对话。
	for _, call := range calls {
		if ctx.Err() != nil {
			break
		}
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
