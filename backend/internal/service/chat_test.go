package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/example/agent-platform/backend/internal/database/model"
	"github.com/example/agent-platform/backend/internal/domain"
	"github.com/example/agent-platform/backend/internal/llmgateway"
	"github.com/example/agent-platform/backend/internal/provider"
	"github.com/example/agent-platform/backend/internal/tool"
)

func TestPrepareResolvesServerOwnedOutputFormat(t *testing.T) {
	chat := NewChat(&fakeChatRepository{}, testGateway(fakeProviderResolver{client: &scriptedNonStreamToolProvider{}}), nil, nil, 4, nil, "", StreamConfig{}, ContextConfig{})
	session, err := chat.Prepare(context.Background(), "user-1", "conversation-1", ChatInput{
		Content: "提取联系人", Provider: "openai", Model: "demo", OutputFormat: "contact_extraction",
	})
	if err != nil {
		t.Fatal(err)
	}
	if session.request.ResponseFormat == nil || session.request.ResponseFormat.Name != "contact_extraction" {
		t.Fatalf("response format = %#v", session.request.ResponseFormat)
	}
}

func TestPrepareRejectsUnknownOutputFormat(t *testing.T) {
	chat := NewChat(&fakeChatRepository{}, testGateway(fakeProviderResolver{client: &scriptedNonStreamToolProvider{}}), nil, nil, 4, nil, "", StreamConfig{}, ContextConfig{})
	_, err := chat.Prepare(context.Background(), "user-1", "conversation-1", ChatInput{
		Content: "hello", Provider: "openai", Model: "demo", OutputFormat: "client_defined_schema",
	})
	if !errors.Is(err, ErrOutputFormatNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestChatServiceMarksDisconnectInterruptedWithoutReplayingProvider(t *testing.T) {
	modelClient := &disconnectingProvider{}
	chat := NewChat(&fakeChatRepository{}, testGateway(fakeProviderResolver{client: modelClient}), nil, nil, 4, nil, "", StreamConfig{}, ContextConfig{})
	session, err := chat.Prepare(context.Background(), "user-1", "conversation-1", ChatInput{Content: "hello", Provider: "openai", Model: "demo"})
	if err != nil {
		t.Fatal(err)
	}

	var streamed string
	completed, status, err := chat.Stream(context.Background(), session, func(event provider.StreamEvent) {
		streamed += event.Delta
	})
	if err != nil {
		t.Fatal(err)
	}
	if status != "interrupted" || completed.Content != "半截" || streamed != "半截" || completed.FinishReason != "disconnected" {
		t.Fatalf("status=%q completed=%q streamed=%q", status, completed.Content, streamed)
	}
	if modelClient.calls != 1 {
		t.Fatalf("calls=%d", modelClient.calls)
	}
}

func TestChatServiceSeparatesTTFTTimeout(t *testing.T) {
	modelClient := &waitingProvider{connected: true}
	chat := NewChat(&fakeChatRepository{}, testGateway(fakeProviderResolver{client: modelClient}), nil, nil, 4, nil, "", StreamConfig{
		TTFTTimeout:  20 * time.Millisecond,
		TotalTimeout: time.Second,
	}, ContextConfig{})
	session, err := chat.Prepare(context.Background(), "user-1", "conversation-1", ChatInput{Content: "hello", Provider: "openai", Model: "demo"})
	if err != nil {
		t.Fatal(err)
	}

	completed, status, err := chat.Stream(context.Background(), session, func(provider.StreamEvent) {})
	if err != nil {
		t.Fatal(err)
	}
	if status != "interrupted" || completed.FinishReason != "ttft_timeout" {
		t.Fatalf("status=%q reason=%q", status, completed.FinishReason)
	}
}

func TestChatServiceSeparatesTotalTimeout(t *testing.T) {
	modelClient := &waitingProvider{connected: true, active: true}
	chat := NewChat(&fakeChatRepository{}, testGateway(fakeProviderResolver{client: modelClient}), nil, nil, 4, nil, "", StreamConfig{
		TTFTTimeout:  time.Second,
		TotalTimeout: 20 * time.Millisecond,
	}, ContextConfig{})
	session, err := chat.Prepare(context.Background(), "user-1", "conversation-1", ChatInput{Content: "hello", Provider: "openai", Model: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	completed, status, err := chat.Stream(context.Background(), session, func(provider.StreamEvent) {})
	if err != nil {
		t.Fatal(err)
	}
	if status != "interrupted" || completed.FinishReason != "total_timeout" {
		t.Fatalf("status=%q reason=%q", status, completed.FinishReason)
	}
}

func TestChatServiceCancellationStopsWithoutReconnect(t *testing.T) {
	modelClient := &cancelableProvider{}
	chat := NewChat(&fakeChatRepository{}, testGateway(fakeProviderResolver{client: modelClient}), nil, nil, 4, nil, "", StreamConfig{}, ContextConfig{})
	session, err := chat.Prepare(context.Background(), "user-1", "conversation-1", ChatInput{Content: "hello", Provider: "openai", Model: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	completed, status, err := chat.Stream(ctx, session, func(event provider.StreamEvent) {
		if event.Delta != "" {
			cancel()
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if status != "stopped" || completed.Content != "部分内容" {
		t.Fatalf("status=%q content=%q", status, completed.Content)
	}
}

type disconnectingProvider struct{ calls int }

func (*disconnectingProvider) Chat(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
	return provider.ChatResponse{}, errors.New("unexpected non-streaming chat")
}

func (p *disconnectingProvider) ChatStream(context.Context, provider.ChatRequest) <-chan provider.StreamEvent {
	p.calls++
	events := make(chan provider.StreamEvent, 2)
	events <- provider.StreamEvent{Delta: "半截"}
	events <- provider.StreamEvent{Err: provider.NewStreamError(provider.StreamErrorDisconnected, errors.New("connection reset"))}
	close(events)
	return events
}

type waitingProvider struct{ connected, active bool }

func (*waitingProvider) Chat(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
	return provider.ChatResponse{}, errors.New("unexpected non-streaming chat")
}

func (p *waitingProvider) ChatStream(ctx context.Context, _ provider.ChatRequest) <-chan provider.StreamEvent {
	events := make(chan provider.StreamEvent)
	go func() {
		defer close(events)
		if p.connected {
			events <- provider.StreamEvent{Connected: true}
		}
		if p.active {
			events <- provider.StreamEvent{Activity: true}
		}
		<-ctx.Done()
	}()
	return events
}

type cancelableProvider struct{}

func (*cancelableProvider) Chat(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
	return provider.ChatResponse{}, errors.New("unexpected non-streaming chat")
}

func (*cancelableProvider) ChatStream(ctx context.Context, _ provider.ChatRequest) <-chan provider.StreamEvent {
	events := make(chan provider.StreamEvent)
	go func() {
		defer close(events)
		select {
		case events <- provider.StreamEvent{Delta: "部分内容"}:
		case <-ctx.Done():
			return
		}
		<-ctx.Done()
	}()
	return events
}

func TestChatServiceExecutesToolAndContinuesModelStream(t *testing.T) {
	modelClient := &scriptedToolProvider{}
	repository := &fakeChatRepository{}
	chat := NewChat(repository, testGateway(fakeProviderResolver{client: modelClient}), tool.NewDefaultRegistry(), nil, 4, nil, "", StreamConfig{}, ContextConfig{})

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

func TestPrepareRegenerationUsesOriginalUserAndStopsContextBeforeOldAnswer(t *testing.T) {
	u1, u2 := "user-1", "user-2"
	repository := &fakeChatRepository{
		conversation: model.Conversation{ID: "conversation-1"},
		history: []model.Message{
			{ID: u1, ConversationID: "conversation-1", Role: "user", Content: "第一问", Status: "complete"},
			{ID: "assistant-1", ConversationID: "conversation-1", Role: "assistant", Content: "第一答", Status: "complete", ReplyToMessageID: &u1, ResponseVersion: 1, IsActiveResponse: true},
			{ID: u2, ConversationID: "conversation-1", Role: "user", Content: "原问题", Status: "complete"},
			{ID: "assistant-2", ConversationID: "conversation-1", Role: "assistant", Content: "旧回答", Provider: "openai", Model: "demo", Status: "complete", ReplyToMessageID: &u2, ResponseVersion: 1, IsActiveResponse: true},
			{ID: "user-3", ConversationID: "conversation-1", Role: "user", Content: "后续问题", Status: "complete"},
		},
	}
	chat := NewChat(repository, testGateway(fakeProviderResolver{client: &scriptedNonStreamToolProvider{}}), nil, nil, 4, nil, "", StreamConfig{}, ContextConfig{})

	session, err := chat.PrepareRegeneration(context.Background(), "account-1", "conversation-1", "assistant-2", RegenerateInput{
		Provider: "openai", Model: "demo", IdempotencyKey: "regenerate-once",
	})
	if err != nil {
		t.Fatal(err)
	}
	if session.UserMessage.ID != u2 || !session.IsRegeneration || session.AssistantMessage.ResponseVersion != 2 {
		t.Fatalf("session = %#v", session)
	}
	if session.AssistantMessage.RegeneratedFrom == nil || *session.AssistantMessage.RegeneratedFrom != "assistant-2" {
		t.Fatalf("regenerated from = %#v", session.AssistantMessage.RegeneratedFrom)
	}
	got := session.request.Messages
	if len(got) != 3 || got[0].Content != "第一问" || got[1].Content != "第一答" || got[2].Content != "原问题" {
		t.Fatalf("request messages = %#v", got)
	}
}

func TestPrepareEditCreatesSiblingBranchAndUsesOnlyAncestors(t *testing.T) {
	rootUser := "root-user"
	rootAssistant := "root-assistant"
	targetUser := "target-user"
	repository := &fakeChatRepository{
		conversation: model.Conversation{ID: "conversation-1"},
		history: []model.Message{
			{ID: rootUser, ConversationID: "conversation-1", Role: "user", Content: "背景", Status: "complete", IsActiveChild: true},
			{ID: rootAssistant, ConversationID: "conversation-1", Role: "assistant", Content: "背景回答", Status: "complete", ParentMessageID: &rootUser, ReplyToMessageID: &rootUser, IsActiveChild: true, IsActiveResponse: true},
			{ID: targetUser, ConversationID: "conversation-1", Role: "user", Content: "原问题", Status: "complete", ParentMessageID: &rootAssistant, IsActiveChild: true},
		},
	}
	chat := NewChat(repository, testGateway(fakeProviderResolver{client: &scriptedNonStreamToolProvider{}}), nil, nil, 4, nil, "", StreamConfig{}, ContextConfig{})

	session, err := chat.PrepareEdit(context.Background(), "account-1", "conversation-1", targetUser, ChatInput{
		Content: "编辑后的问题", Provider: "openai", Model: "demo", IdempotencyKey: "edit-once",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !session.IsEdit || session.UserMessage.Content != "编辑后的问题" || session.UserMessage.EditedFrom == nil || *session.UserMessage.EditedFrom != targetUser {
		t.Fatalf("session = %#v", session)
	}
	if session.UserMessage.ParentMessageID == nil || *session.UserMessage.ParentMessageID != rootAssistant {
		t.Fatalf("edited parent = %#v", session.UserMessage.ParentMessageID)
	}
	got := session.request.Messages
	if len(got) != 3 || got[0].Content != "背景" || got[1].Content != "背景回答" || got[2].Content != "编辑后的问题" {
		t.Fatalf("request messages = %#v", got)
	}
	if repository.history[2].Content != "原问题" {
		t.Fatalf("original message was modified: %#v", repository.history[2])
	}
}

func TestConcurrentEditsPreserveBothBranchesAndSelectOne(t *testing.T) {
	targetID := "root-user"
	repository := &fakeChatRepository{
		conversation: model.Conversation{ID: "conversation-1"},
		history: []model.Message{{
			ID: targetID, ConversationID: "conversation-1", Role: "user", Content: "原问题", Status: "complete", IsActiveChild: true,
		}},
	}
	chat := NewChat(repository, testGateway(fakeProviderResolver{client: &scriptedNonStreamToolProvider{}}), nil, nil, 4, nil, "", StreamConfig{}, ContextConfig{})

	var wait sync.WaitGroup
	errorsFound := make(chan error, 2)
	for index := 1; index <= 2; index++ {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := chat.PrepareEdit(context.Background(), "account-1", "conversation-1", targetID, ChatInput{
				Content: fmt.Sprintf("编辑版本 %d", index), Provider: "openai", Model: "demo", IdempotencyKey: fmt.Sprintf("edit-%d", index),
			})
			errorsFound <- err
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}

	repository.mu.Lock()
	defer repository.mu.Unlock()
	editedCount, activeCount := 0, 0
	for _, message := range repository.history {
		if message.Role != "user" || message.EditedFrom == nil {
			continue
		}
		editedCount++
		if message.IsActiveChild {
			activeCount++
		}
	}
	if editedCount != 2 || activeCount != 1 || repository.history[0].Content != "原问题" {
		t.Fatalf("edited=%d active=%d history=%#v", editedCount, activeCount, repository.history)
	}
}

type fakeChatRepository struct {
	mu                      sync.Mutex
	conversation            model.Conversation
	history                 []model.Message
	summary                 string
	summaryThroughMessageID string
	summaryVersions         []model.ConversationSummaryVersion
	lastCompletion          model.MessageCompletion
	editCount               int
}

func (f *fakeChatRepository) FindByIDForUser(_ context.Context, _ string, id string) (model.Conversation, error) {
	if f.conversation.ID != "" {
		return f.conversation, nil
	}
	return model.Conversation{ID: id}, nil
}

func (f *fakeChatRepository) ListByConversationForUser(context.Context, string, string) ([]model.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]model.Message(nil), f.history...), nil
}

func (f *fakeChatRepository) FindMessageByIDForUser(_ context.Context, _, _ string, messageID string) (model.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, message := range f.history {
		if message.ID == messageID {
			return message, nil
		}
	}
	return model.Message{}, domain.ErrNotFound
}

func (f *fakeChatRepository) SaveConversationSummaryVersion(_ context.Context, _ string, version *model.ConversationSummaryVersion, activate bool) error {
	version.Version = int64(len(f.summaryVersions) + 1)
	f.summaryVersions = append(f.summaryVersions, *version)
	if activate {
		f.summary = version.Summary
		f.summaryThroughMessageID = version.ThroughMessageID
	}
	return nil
}

func (f *fakeChatRepository) CreateTurn(_ context.Context, user, assistant *model.Message) error {
	user.ID = "message-user"
	user.IsActiveChild = true
	assistant.ID = "message-assistant"
	assistant.ParentMessageID = &user.ID
	assistant.ReplyToMessageID = &user.ID
	assistant.ResponseVersion = 1
	assistant.IsActiveResponse = true
	assistant.IsActiveChild = true
	return nil
}

func (f *fakeChatRepository) CreateRegeneration(_ context.Context, _ string, targetMessageID, replyToMessageID string, assistant *model.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	latest := int64(1)
	for index := range f.history {
		message := &f.history[index]
		if message.Role == "assistant" && message.ReplyToMessageID != nil && *message.ReplyToMessageID == replyToMessageID {
			message.IsActiveResponse = false
			if message.ResponseVersion > latest {
				latest = message.ResponseVersion
			}
		}
	}
	assistant.ID = "message-regenerated"
	assistant.ParentMessageID = &replyToMessageID
	assistant.ReplyToMessageID = &replyToMessageID
	assistant.ResponseVersion = latest + 1
	assistant.IsActiveResponse = true
	assistant.IsActiveChild = true
	assistant.RegeneratedFrom = &targetMessageID
	f.history = append(f.history, *assistant)
	return nil
}

func (f *fakeChatRepository) CreateEditedTurn(_ context.Context, _ string, targetMessageID string, user, assistant *model.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for index := range f.history {
		message := &f.history[index]
		if message.Role == "user" && sameMessageParent(message.ParentMessageID, user.ParentMessageID) {
			message.IsActiveChild = false
		}
	}
	f.editCount++
	user.ID = fmt.Sprintf("message-edited-user-%d", f.editCount)
	user.IsActiveChild = true
	user.EditedFrom = &targetMessageID
	assistant.ID = fmt.Sprintf("message-edited-assistant-%d", f.editCount)
	assistant.ParentMessageID = &user.ID
	assistant.ReplyToMessageID = &user.ID
	assistant.ResponseVersion = 1
	assistant.IsActiveResponse = true
	assistant.IsActiveChild = true
	f.history = append(f.history, *user, *assistant)
	return nil
}

func sameMessageParent(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func (f *fakeChatRepository) Finalize(_ context.Context, id string, completion model.MessageCompletion) (model.Message, error) {
	f.lastCompletion = completion
	return model.Message{
		ID:               id,
		Content:          completion.Content,
		Think:            completion.Think,
		StructuredOutput: completion.StructuredOutput,
		Status:           completion.Status,
		FinishReason:     completion.FinishReason,
		InputTokens:      completion.Usage.InputTokens,
		OutputTokens:     completion.Usage.OutputTokens,
		TotalTokens:      completion.Usage.TotalTokens,
		UsageReported:    completion.Usage.UsageReported,
		CostNanoUSD:      completion.Usage.CostNanoUSD,
		CostKnown:        completion.Usage.CostKnown,
	}, nil
}

type fakeProviderResolver struct {
	client provider.Provider
	limits provider.ModelLimits
}

func (f fakeProviderResolver) Get(string, string) (provider.Provider, error) { return f.client, nil }
func (fakeProviderResolver) SupportsTools(string) bool                       { return true }
func (fakeProviderResolver) SupportsThinking(string, string) bool            { return false }
func (fakeProviderResolver) CalculateCost(string, string, provider.TokenUsage) (int64, bool) {
	return 99, true
}
func (f fakeProviderResolver) Limits(string, string) (provider.ModelLimits, bool) {
	if f.limits.ContextWindow > 0 {
		return f.limits, true
	}
	return provider.ModelLimits{ContextWindow: 1_000_000, MaxOutputTokens: 4096}, true
}
func (fakeProviderResolver) Tokenizer(string, string) string { return "heuristic" }

func testGateway(catalog llmgateway.Catalog) llmgateway.Gateway {
	return llmgateway.New(catalog, nil, nil, llmgateway.NewMemoryIdempotencyStore(time.Hour), nil, llmgateway.Config{
		TokenBudget: 1_000_000, MaxOutputTokens: 1,
	})
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
	chat := NewChat(&fakeChatRepository{}, testGateway(fakeProviderResolver{client: modelClient}), tool.NewDefaultRegistry(), nil, 4, nil, "", StreamConfig{}, ContextConfig{})

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
	chat := NewChat(&fakeChatRepository{}, testGateway(resolver), tool.NewDefaultRegistry(), nil, 4, &provider.ThinkingConfig{Enabled: true, BudgetTokens: 2048}, "", StreamConfig{}, ContextConfig{})

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
func (thinkingProviderResolver) Limits(string, string) (provider.ModelLimits, bool) {
	return provider.ModelLimits{ContextWindow: 1_000_000, MaxOutputTokens: 4096}, true
}
func (thinkingProviderResolver) Tokenizer(string, string) string { return "heuristic" }
