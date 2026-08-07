package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/example/agent-platform/backend/internal/database/model"
	"github.com/example/agent-platform/backend/internal/llmgateway"
	"github.com/example/agent-platform/backend/internal/provider"
)

func TestContextHistoryExcludesInactiveResponseVersions(t *testing.T) {
	userID := "user-message"
	messages := []model.Message{
		{ID: userID, Role: "user", Content: "问题", Status: "complete"},
		{ID: "answer-v1", Role: "assistant", Content: "旧回答", Status: "complete", ReplyToMessageID: &userID, ResponseVersion: 1, IsActiveResponse: false},
		{ID: "later-user", Role: "user", Content: "后续问题", Status: "complete"},
		{ID: "answer-v2", Role: "assistant", Content: "当前回答", Status: "complete", ReplyToMessageID: &userID, ResponseVersion: 2, IsActiveResponse: true},
	}

	_, history := contextHistory(model.Conversation{}, messages)
	if len(history) != 3 || history[0].ID != userID || history[1].ID != "answer-v2" || history[2].ID != "later-user" {
		t.Fatalf("history = %#v", history)
	}
}

func TestContextHistoryFollowsOnlySelectedConversationBranch(t *testing.T) {
	rootUser := "root-user"
	rootAssistant := "root-assistant"
	oldUser := "old-user"
	newUser := "new-user"
	messages := []model.Message{
		{ID: rootUser, Role: "user", Content: "背景", Status: "complete", IsActiveChild: true},
		{ID: rootAssistant, Role: "assistant", Content: "背景回答", Status: "complete", ParentMessageID: &rootUser, IsActiveChild: true},
		{ID: oldUser, Role: "user", Content: "旧问题", Status: "complete", ParentMessageID: &rootAssistant, IsActiveChild: false},
		{ID: "old-answer", Role: "assistant", Content: "旧分支回答", Status: "complete", ParentMessageID: &oldUser, IsActiveChild: true},
		{ID: newUser, Role: "user", Content: "新问题", Status: "complete", ParentMessageID: &rootAssistant, IsActiveChild: true},
		{ID: "new-answer", Role: "assistant", Content: "新分支回答", Status: "complete", ParentMessageID: &newUser, IsActiveChild: true},
	}

	_, history := contextHistory(model.Conversation{}, messages)
	want := []string{rootUser, rootAssistant, newUser, "new-answer"}
	if len(history) != len(want) {
		t.Fatalf("history = %#v", history)
	}
	for index := range want {
		if history[index].ID != want[index] {
			t.Fatalf("history[%d] = %q, want %q", index, history[index].ID, want[index])
		}
	}
}

func TestContextManagementSummarizesOldHistoryAndAccountsUsage(t *testing.T) {
	repository := &fakeChatRepository{history: contextTestHistory(10, 24)}
	client := &contextTestProvider{}
	gateway := contextTestGateway(client, provider.ModelLimits{ContextWindow: 220, MaxOutputTokens: 30})
	chat := NewChat(repository, gateway, nil, nil, 4, nil, "system instruction", StreamConfig{}, ContextConfig{
		SummaryEnabled: true, SummaryMaxTokens: 20, KeepRecentMessages: 2, SummaryMaxCalls: 3,
	})

	session, err := chat.Prepare(context.Background(), "user-1", "conversation-1", ChatInput{Content: "当前问题", Provider: "demo", Model: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if client.summaryCalls == 0 || repository.summary == "" || repository.summaryThroughMessageID == "" {
		t.Fatalf("summary calls=%d summary=%q through=%q", client.summaryCalls, repository.summary, repository.summaryThroughMessageID)
	}
	budget, err := gateway.TokenBudget("demo", "demo", 0)
	if err != nil {
		t.Fatal(err)
	}
	estimated, err := gateway.EstimateInputTokens("demo", session.request)
	if err != nil {
		t.Fatal(err)
	}
	if estimated > budget.InputTokenLimit {
		t.Fatalf("input tokens=%d limit=%d", estimated, budget.InputTokenLimit)
	}
	if !containsSystemSummary(session.request.Messages) || session.request.Messages[len(session.request.Messages)-1].Content != "当前问题" {
		t.Fatalf("messages = %#v", session.request.Messages)
	}

	completed, err := chat.Chat(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	wantTotal := int64(client.summaryCalls*15 + 5)
	if !completed.UsageReported || completed.TotalTokens != wantTotal {
		t.Fatalf("usage=%#v want total=%d", completed, wantTotal)
	}
}

func TestContextManagementFallsBackToRecentMessagesWhenSummaryFails(t *testing.T) {
	repository := &fakeChatRepository{history: contextTestHistory(8, 24)}
	client := &contextTestProvider{summaryErr: errors.New("summary unavailable")}
	gateway := contextTestGateway(client, provider.ModelLimits{ContextWindow: 220, MaxOutputTokens: 30})
	chat := NewChat(repository, gateway, nil, nil, 4, nil, "system", StreamConfig{}, ContextConfig{
		SummaryEnabled: true, SummaryMaxTokens: 20, KeepRecentMessages: 2, SummaryMaxCalls: 2,
	})

	session, err := chat.Prepare(context.Background(), "user-1", "conversation-1", ChatInput{Content: "继续", Provider: "demo", Model: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if client.summaryCalls != 1 || repository.summary != "" || containsSystemSummary(session.request.Messages) {
		t.Fatalf("calls=%d persisted=%q messages=%#v", client.summaryCalls, repository.summary, session.request.Messages)
	}
	budget, _ := gateway.TokenBudget("demo", "demo", 0)
	estimated, _ := gateway.EstimateInputTokens("demo", session.request)
	if estimated > budget.InputTokenLimit {
		t.Fatalf("fallback tokens=%d limit=%d", estimated, budget.InputTokenLimit)
	}
	if len(session.request.Messages) >= len(repository.history)+2 {
		t.Fatalf("history was not trimmed: messages=%d history=%d", len(session.request.Messages), len(repository.history))
	}
}

func TestContextManagementReusesPersistedSummaryBoundary(t *testing.T) {
	history := contextTestHistory(6, 8)
	through := history[3].ID
	repository := &fakeChatRepository{
		conversation: model.Conversation{ID: "conversation-1", Summary: "用户偏好简洁回答", SummaryThroughMessageID: &through},
		history:      history,
	}
	client := &contextTestProvider{}
	gateway := contextTestGateway(client, provider.ModelLimits{ContextWindow: 220, MaxOutputTokens: 30})
	chat := NewChat(repository, gateway, nil, nil, 4, nil, "system", StreamConfig{}, ContextConfig{
		SummaryEnabled: true, SummaryMaxTokens: 20, KeepRecentMessages: 2, SummaryMaxCalls: 2,
	})

	session, err := chat.Prepare(context.Background(), "user-1", "conversation-1", ChatInput{Content: "继续", Provider: "demo", Model: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if client.summaryCalls != 0 || !containsSystemSummary(session.request.Messages) {
		t.Fatalf("calls=%d messages=%#v", client.summaryCalls, session.request.Messages)
	}
	for _, message := range session.request.Messages {
		if message.Content == history[0].Content || message.Content == history[3].Content {
			t.Fatalf("already summarized history leaked into context: %#v", session.request.Messages)
		}
	}
}

func TestContextManagementRejectsCurrentMessageThatCannotFit(t *testing.T) {
	repository := &fakeChatRepository{}
	client := &contextTestProvider{}
	gateway := contextTestGateway(client, provider.ModelLimits{ContextWindow: 100, MaxOutputTokens: 20})
	chat := NewChat(repository, gateway, nil, nil, 4, nil, "system", StreamConfig{}, ContextConfig{})

	_, err := chat.Prepare(context.Background(), "user-1", "conversation-1", ChatInput{
		Content: strings.Repeat("很长的问题", 30), Provider: "demo", Model: "demo",
	})
	if !errors.Is(err, llmgateway.ErrTokenBudgetExceeded) {
		t.Fatalf("err = %v", err)
	}
	if repository.lastCompletion.Status != "error" || repository.lastCompletion.FinishReason != "token_budget_exceeded" {
		t.Fatalf("completion = %#v", repository.lastCompletion)
	}
}

func TestContextManagementTrimsLongChineseAndEnglishHistoryOnTurnBoundary(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		content string
	}{
		{name: "Chinese", content: strings.Repeat("上下文", 20)},
		{name: "English", content: strings.Repeat("long conversation context ", 12)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			history := make([]model.Message, 0, 12)
			for index := 0; index < 12; index++ {
				role := "user"
				if index%2 == 1 {
					role = "assistant"
				}
				history = append(history, model.Message{ID: fmt.Sprintf("message-%02d", index), Role: role, Status: "complete", Content: testCase.content})
			}
			repository := &fakeChatRepository{history: history}
			gateway := contextTestGateway(&contextTestProvider{}, provider.ModelLimits{ContextWindow: 180, MaxOutputTokens: 30})
			chat := NewChat(repository, gateway, nil, nil, 4, nil, "system", StreamConfig{}, ContextConfig{})

			session, err := chat.Prepare(context.Background(), "user-1", "conversation-1", ChatInput{Content: "current", Provider: "demo", Model: "demo"})
			if err != nil {
				t.Fatal(err)
			}
			if len(session.request.Messages) < 2 || session.request.Messages[1].Role != "user" {
				t.Fatalf("trimmed history starts with an orphan response: %#v", session.request.Messages)
			}
			budget, _ := gateway.TokenBudget("demo", "demo", 0)
			estimated, _ := gateway.EstimateInputTokens("demo", session.request)
			if estimated > budget.InputTokenLimit {
				t.Fatalf("tokens=%d limit=%d", estimated, budget.InputTokenLimit)
			}
		})
	}
}

func TestContextManagementUsesIndependentSummaryModelAndTracksQualityVersion(t *testing.T) {
	repository := &fakeChatRepository{history: contextTestHistory(10, 24)}
	conversationClient := &contextTestProvider{}
	summaryClient := &qualitySummaryProvider{score: 92, passed: true}
	resolver := routingProviderResolver{clients: map[string]provider.Provider{"chat": conversationClient, "summary": summaryClient}}
	gateway := llmgateway.New(resolver, llmgateway.HeuristicTokenEstimator{}, nil, llmgateway.NewMemoryIdempotencyStore(time.Hour), nil,
		llmgateway.Config{TokenBudget: 2_000, MaxOutputTokens: 30})
	chat := NewChat(repository, gateway, nil, nil, 4, nil, "system", StreamConfig{}, ContextConfig{
		SummaryEnabled: true, SummaryProvider: "summary", SummaryModel: "small",
		SummaryMaxTokens: 20, KeepRecentMessages: 2, SummaryMaxCalls: 2,
		QualityEnabled: true, QualityThreshold: 70, QualityMaxTokens: 20,
	})

	session, err := chat.Prepare(context.Background(), "user-1", "conversation-1", ChatInput{Content: "继续", Provider: "chat", Model: "large"})
	if err != nil {
		t.Fatal(err)
	}
	if summaryClient.summaryCalls == 0 || summaryClient.qualityCalls == 0 || repository.summary == "" || len(repository.summaryVersions) == 0 {
		t.Fatalf("summary=%d quality=%d active=%q versions=%#v", summaryClient.summaryCalls, summaryClient.qualityCalls, repository.summary, repository.summaryVersions)
	}
	version := repository.summaryVersions[0]
	if version.Provider != "summary" || version.Model != "small" || version.Status != "accepted" || !version.QualityPassed || version.QualityScore != 92 || version.PromptVersion != summaryPromptVersion || version.EvaluationPromptVersion != summaryEvaluationPromptVersion {
		t.Fatalf("version = %#v", version)
	}

	completed, err := chat.Chat(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	preparationTokens := int64(summaryClient.summaryCalls*15 + summaryClient.qualityCalls*6)
	if completed.TotalTokens != preparationTokens+5 {
		t.Fatalf("total tokens=%d want=%d", completed.TotalTokens, preparationTokens+5)
	}
	wantCost := preparationTokens*10 + 5*100
	if !completed.CostKnown || completed.CostNanoUSD != wantCost {
		t.Fatalf("cost=%d known=%t want=%d", completed.CostNanoUSD, completed.CostKnown, wantCost)
	}
}

func TestContextManagementRejectsLowQualitySummaryAndKeepsVersion(t *testing.T) {
	repository := &fakeChatRepository{history: contextTestHistory(8, 24)}
	summaryClient := &qualitySummaryProvider{score: 40, passed: true}
	resolver := routingProviderResolver{clients: map[string]provider.Provider{"chat": &contextTestProvider{}, "summary": summaryClient}}
	gateway := llmgateway.New(resolver, llmgateway.HeuristicTokenEstimator{}, nil, llmgateway.NewMemoryIdempotencyStore(time.Hour), nil,
		llmgateway.Config{TokenBudget: 2_000, MaxOutputTokens: 30})
	chat := NewChat(repository, gateway, nil, nil, 4, nil, "system", StreamConfig{}, ContextConfig{
		SummaryEnabled: true, SummaryProvider: "summary", SummaryModel: "small",
		SummaryMaxTokens: 20, KeepRecentMessages: 2, SummaryMaxCalls: 2,
		QualityEnabled: true, QualityThreshold: 70, QualityMaxTokens: 20,
	})

	session, err := chat.Prepare(context.Background(), "user-1", "conversation-1", ChatInput{Content: "继续", Provider: "chat", Model: "large"})
	if err != nil {
		t.Fatal(err)
	}
	if repository.summary != "" || len(repository.summaryVersions) != 1 || repository.summaryVersions[0].Status != "rejected" {
		t.Fatalf("active=%q versions=%#v", repository.summary, repository.summaryVersions)
	}
	if containsSystemSummary(session.request.Messages) {
		t.Fatalf("rejected summary entered final context: %#v", session.request.Messages)
	}
}

func contextTestGateway(client provider.Provider, limits provider.ModelLimits) llmgateway.Gateway {
	return llmgateway.New(
		fakeProviderResolver{client: client, limits: limits}, llmgateway.HeuristicTokenEstimator{}, nil,
		llmgateway.NewMemoryIdempotencyStore(time.Hour), nil,
		llmgateway.Config{TokenBudget: limits.ContextWindow, MaxOutputTokens: limits.MaxOutputTokens},
	)
}

func contextTestHistory(count, contentRunes int) []model.Message {
	history := make([]model.Message, 0, count)
	for index := 0; index < count; index++ {
		role := "user"
		if index%2 == 1 {
			role = "assistant"
		}
		history = append(history, model.Message{
			ID: fmt.Sprintf("message-%02d", index), Role: role, Status: "complete",
			Content: fmt.Sprintf("历史%d-%s", index, strings.Repeat("内", contentRunes)),
		})
	}
	return history
}

func containsSystemSummary(messages []provider.Message) bool {
	for _, message := range messages {
		if message.Role == "system" && strings.HasPrefix(message.Content, "Earlier conversation summary:") {
			return true
		}
	}
	return false
}

type contextTestProvider struct {
	summaryCalls int
	summaryErr   error
}

func (p *contextTestProvider) Chat(_ context.Context, request provider.ChatRequest) (provider.ChatResponse, error) {
	for _, message := range request.Messages {
		if message.Role == "system" && message.Content == summarySystemPrompt {
			p.summaryCalls++
			if p.summaryErr != nil {
				return provider.ChatResponse{}, p.summaryErr
			}
			return provider.ChatResponse{
				Content: fmt.Sprintf("第%d段历史摘要", p.summaryCalls),
				Usage:   &provider.TokenUsage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
			}, nil
		}
	}
	return provider.ChatResponse{Content: "最终回答", Usage: &provider.TokenUsage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}}, nil
}

func (*contextTestProvider) ChatStream(context.Context, provider.ChatRequest) <-chan provider.StreamEvent {
	events := make(chan provider.StreamEvent, 1)
	events <- provider.StreamEvent{Err: errors.New("unexpected stream call")}
	close(events)
	return events
}

type qualitySummaryProvider struct {
	summaryCalls int
	qualityCalls int
	score        int
	passed       bool
}

func (p *qualitySummaryProvider) Chat(_ context.Context, request provider.ChatRequest) (provider.ChatResponse, error) {
	for _, message := range request.Messages {
		switch message.Content {
		case summarySystemPrompt:
			p.summaryCalls++
			return provider.ChatResponse{Content: "独立小模型生成的摘要", Usage: &provider.TokenUsage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}}, nil
		case summaryEvaluationSystemPrompt:
			p.qualityCalls++
			return provider.ChatResponse{Content: fmt.Sprintf(`{"passed":%t,"score":%d,"issues":[]}`, p.passed, p.score), Usage: &provider.TokenUsage{InputTokens: 4, OutputTokens: 2, TotalTokens: 6}}, nil
		}
	}
	return provider.ChatResponse{}, errors.New("unexpected summary model request")
}

func (*qualitySummaryProvider) ChatStream(context.Context, provider.ChatRequest) <-chan provider.StreamEvent {
	events := make(chan provider.StreamEvent)
	close(events)
	return events
}

type routingProviderResolver struct {
	clients map[string]provider.Provider
}

func (r routingProviderResolver) Get(providerID, _ string) (provider.Provider, error) {
	client := r.clients[providerID]
	if client == nil {
		return nil, fmt.Errorf("unknown provider %q", providerID)
	}
	return client, nil
}
func (routingProviderResolver) SupportsTools(string) bool            { return false }
func (routingProviderResolver) SupportsThinking(string, string) bool { return false }
func (routingProviderResolver) CalculateCost(providerID, _ string, usage provider.TokenUsage) (int64, bool) {
	if providerID == "summary" {
		return usage.TotalTokens * 10, true
	}
	return usage.TotalTokens * 100, true
}
func (routingProviderResolver) Limits(providerID, _ string) (provider.ModelLimits, bool) {
	if providerID == "summary" {
		return provider.ModelLimits{ContextWindow: 2_000, MaxOutputTokens: 30}, true
	}
	return provider.ModelLimits{ContextWindow: 220, MaxOutputTokens: 30}, true
}
func (routingProviderResolver) Tokenizer(string, string) string { return "heuristic" }
