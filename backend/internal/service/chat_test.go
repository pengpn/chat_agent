package service

import (
	"context"
	"errors"
	"testing"

	"github.com/example/agent-platform/backend/internal/database/model"
	"github.com/example/agent-platform/backend/internal/provider"
	"github.com/example/agent-platform/backend/internal/tool"
)

func TestChatServiceExecutesToolAndContinuesModelStream(t *testing.T) {
	modelClient := &scriptedToolProvider{}
	repository := &fakeChatRepository{}
	chat := NewChat(repository, fakeProviderResolver{client: modelClient}, tool.NewDefaultRegistry(), 4, nil, "")

	session, err := chat.Prepare(context.Background(), "user-1", "conversation-1", ChatInput{Content: "2 加 2 等于多少？", Provider: "openai", Model: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	var streamed string
	completed, status, err := chat.Stream(context.Background(), session, func(event provider.StreamEvent) { streamed += event.Delta })
	if err != nil {
		t.Fatal(err)
	}
	if status != "complete" || completed.Content != "4" || completed.Think != "先计算。结果为 4。" || streamed != "4" {
		t.Fatalf("status=%q completed=%#v streamed=%q", status, completed, streamed)
	}
	if completed.InputTokens != 6 || completed.OutputTokens != 3 || completed.TotalTokens != 9 || !completed.UsageReported {
		t.Fatalf("usage = %#v", completed)
	}
	if !completed.CostKnown || completed.CostNanoUSD != 99 {
		t.Fatalf("cost = %#v", completed)
	}
	if len(modelClient.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(modelClient.requests))
	}
	if len(modelClient.requests[0].Tools) != 1 || modelClient.requests[0].Tools[0].Function.Name != "calculator" {
		t.Fatalf("initial tools = %#v", modelClient.requests[0].Tools)
	}
	secondRequest := modelClient.requests[1]
	if len(secondRequest.Messages) != 3 || secondRequest.Messages[1].Role != "assistant" || secondRequest.Messages[1].ReasoningContent != "先计算。" || len(secondRequest.Messages[1].ToolCalls) != 1 || secondRequest.Messages[2].Role != "tool" || secondRequest.Messages[2].Content != `{"result":4}` {
		t.Fatalf("second request messages = %#v", secondRequest.Messages)
	}
}

type fakeChatRepository struct{}

func (f *fakeChatRepository) FindByIDForUser(_ context.Context, _ string, id string) (model.Conversation, error) {
	return model.Conversation{ID: id}, nil
}

func (f *fakeChatRepository) ListByConversationForUser(context.Context, string, string) ([]model.Message, error) {
	return nil, nil
}

func (f *fakeChatRepository) CreateTurn(_ context.Context, user, assistant *model.Message) error {
	user.ID = "message-user"
	assistant.ID = "message-assistant"
	return nil
}

func (f *fakeChatRepository) Finalize(_ context.Context, id, content, think, status string, usage model.MessageUsage) (model.Message, error) {
	return model.Message{
		ID:            id,
		Content:       content,
		Think:         think,
		Status:        status,
		InputTokens:   usage.InputTokens,
		OutputTokens:  usage.OutputTokens,
		TotalTokens:   usage.TotalTokens,
		UsageReported: usage.UsageReported,
		CostNanoUSD:   usage.CostNanoUSD,
		CostKnown:     usage.CostKnown,
	}, nil
}

type fakeProviderResolver struct{ client provider.Provider }

func (f fakeProviderResolver) Get(string, string) (provider.Provider, error) { return f.client, nil }
func (fakeProviderResolver) SupportsTools(string) bool                       { return true }
func (fakeProviderResolver) SupportsThinking(string, string) bool            { return false }
func (fakeProviderResolver) CalculateCost(string, string, provider.TokenUsage) (int64, bool) {
	return 99, true
}

type scriptedToolProvider struct{ requests []provider.ChatRequest }

func (*scriptedToolProvider) Chat(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
	return provider.ChatResponse{}, errors.New("unexpected non-streaming chat")
}

func (s *scriptedToolProvider) ChatStream(_ context.Context, request provider.ChatRequest) <-chan provider.StreamEvent {
	s.requests = append(s.requests, request)
	events := make(chan provider.StreamEvent, 4)
	if len(s.requests) == 1 {
		events <- provider.StreamEvent{ThinkDelta: "先计算。"}
		events <- provider.StreamEvent{ToolCalls: []provider.ToolCall{{
			ID:       "call_1",
			Type:     "function",
			Function: provider.ToolFunctionCall{Name: "calculator", Arguments: `{"expression":"2+2"}`},
		}}}
		events <- provider.StreamEvent{Usage: &provider.TokenUsage{InputTokens: 2, OutputTokens: 1, TotalTokens: 3}}
	} else {
		events <- provider.StreamEvent{ThinkDelta: "结果为 4。"}
		events <- provider.StreamEvent{Delta: "4"}
		events <- provider.StreamEvent{Usage: &provider.TokenUsage{InputTokens: 4, OutputTokens: 2, TotalTokens: 6}}
	}
	events <- provider.StreamEvent{Done: true}
	close(events)
	return events
}

func TestChatServiceExecutesToolAndReturnsCompleteResult(t *testing.T) {
	modelClient := &scriptedNonStreamToolProvider{}
	chat := NewChat(&fakeChatRepository{}, fakeProviderResolver{client: modelClient}, tool.NewDefaultRegistry(), 4, nil, "")

	session, err := chat.Prepare(context.Background(), "user-1", "conversation-1", ChatInput{Content: "2 加 2 等于多少？", Provider: "openai", Model: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	completed, err := chat.Chat(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != "complete" || completed.Content != "4" || completed.Think != "先计算。结果为 4。" {
		t.Fatalf("completed = %#v", completed)
	}
	if completed.InputTokens != 6 || completed.OutputTokens != 3 || completed.TotalTokens != 9 || !completed.UsageReported {
		t.Fatalf("usage = %#v", completed)
	}
	if len(modelClient.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(modelClient.requests))
	}
	secondRequest := modelClient.requests[1]
	if len(secondRequest.Messages) != 3 || secondRequest.Messages[1].Role != "assistant" || secondRequest.Messages[1].ReasoningContent != "先计算。" || secondRequest.Messages[2].Role != "tool" || secondRequest.Messages[2].Content != `{"result":4}` {
		t.Fatalf("second request messages = %#v", secondRequest.Messages)
	}
}

type scriptedNonStreamToolProvider struct{ requests []provider.ChatRequest }

func (s *scriptedNonStreamToolProvider) Chat(_ context.Context, request provider.ChatRequest) (provider.ChatResponse, error) {
	s.requests = append(s.requests, request)
	if len(s.requests) == 1 {
		return provider.ChatResponse{
			Think: "先计算。",
			ToolCalls: []provider.ToolCall{{
				ID:       "call_1",
				Type:     "function",
				Function: provider.ToolFunctionCall{Name: "calculator", Arguments: `{"expression":"2+2"}`},
			}},
			Usage: &provider.TokenUsage{InputTokens: 2, OutputTokens: 1, TotalTokens: 3},
		}, nil
	}
	return provider.ChatResponse{
		Content: "4",
		Think:   "结果为 4。",
		Usage:   &provider.TokenUsage{InputTokens: 4, OutputTokens: 2, TotalTokens: 6},
	}, nil
}

func (*scriptedNonStreamToolProvider) ChatStream(_ context.Context, _ provider.ChatRequest) <-chan provider.StreamEvent {
	events := make(chan provider.StreamEvent, 1)
	events <- provider.StreamEvent{Err: errors.New("unexpected streaming chat")}
	close(events)
	return events
}

func TestPrepareEnablesThinkingForCompatibleProvider(t *testing.T) {
	resolver := thinkingProviderResolver{client: &scriptedToolProvider{}, supported: true}
	chat := NewChat(&fakeChatRepository{}, resolver, tool.NewDefaultRegistry(), 4, &provider.ThinkingConfig{Enabled: true, BudgetTokens: 2048}, "")

	session, err := chat.Prepare(context.Background(), "user-1", "conversation-1", ChatInput{Content: "请分析", Provider: "deepseek", Model: "deepseek-v4-flash"})
	if err != nil {
		t.Fatal(err)
	}
	if session.request.Thinking == nil || !session.request.Thinking.Enabled || session.request.Thinking.BudgetTokens != 2048 {
		t.Fatalf("thinking = %#v", session.request.Thinking)
	}
}

type thinkingProviderResolver struct {
	client    provider.Provider
	supported bool
}

func (r thinkingProviderResolver) Get(string, string) (provider.Provider, error) {
	return r.client, nil
}
func (thinkingProviderResolver) SupportsTools(string) bool              { return true }
func (r thinkingProviderResolver) SupportsThinking(string, string) bool { return r.supported }
func (thinkingProviderResolver) CalculateCost(string, string, provider.TokenUsage) (int64, bool) {
	return 0, false
}
