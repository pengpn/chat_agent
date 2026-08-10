package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/example/agent-platform/backend/internal/database/model"
	"github.com/example/agent-platform/backend/internal/provider"
	"github.com/example/agent-platform/backend/internal/repository"
)

func TestTitleServiceGeneratesSanitizedTitleAndRecordsDedicatedUsage(t *testing.T) {
	answerID := "answer-1"
	userID := "message-user-1"
	client := &titleTestProvider{response: provider.ChatResponse{
		Content: `## 标题：**Go Agent 入门**`,
		Usage:   &provider.TokenUsage{InputTokens: 20, OutputTokens: 5, TotalTokens: 25},
	}}
	repo := newTitleTestRepository(userID, answerID)
	events := &titleEventRecorder{}
	service := NewTitle(repo, testGateway(fakeProviderResolver{client: client}), events, TitleConfig{MaxLength: 20})

	conversation, err := service.Generate(context.Background(), "account-1", "conversation-1")
	if err != nil {
		t.Fatal(err)
	}
	if conversation.Title != "Go Agent 入门" || conversation.TitleStatus != "generated" {
		t.Fatalf("conversation = %#v", conversation)
	}
	if repo.usage.InputTokens != 20 || repo.usage.OutputTokens != 5 || repo.usage.TotalTokens != 25 || !repo.usage.UsageReported {
		t.Fatalf("usage = %#v", repo.usage)
	}
	if repo.usage.CostNanoUSD != 99 || !repo.usage.CostKnown || client.calls != 1 {
		t.Fatalf("usage=%#v calls=%d", repo.usage, client.calls)
	}
	if events.userID != "account-1" || events.name != "conversation.title.updated" || events.payload["conversation"] == nil {
		t.Fatalf("event = %#v", events)
	}
}

func TestTitleServiceFallsBackToFirstQuestionWhenModelFails(t *testing.T) {
	client := &titleTestProvider{err: errors.New("provider unavailable")}
	repo := newTitleTestRepository("message-user-1", "answer-1")
	repo.claim.UserMessage.Content = "# 如何学习 **AI Agent**？\n请给一条路线"
	service := NewTitle(repo, testGateway(fakeProviderResolver{client: client}), nil, TitleConfig{MaxLength: 40})

	conversation, err := service.Generate(context.Background(), "account-1", "conversation-1")
	if err != nil {
		t.Fatal(err)
	}
	if conversation.Title != "如何学习 AI Agent？ 请给一条路线" || conversation.TitleStatus != "fallback" {
		t.Fatalf("conversation = %#v", conversation)
	}
}

func TestTitleServiceDoesNotOverwriteManualRenameOrGenerateTwice(t *testing.T) {
	client := &titleTestProvider{response: provider.ChatResponse{Content: "模型标题"}}
	repo := newTitleTestRepository("message-user-1", "answer-1")
	repo.manualBeforeComplete = true
	service := NewTitle(repo, testGateway(fakeProviderResolver{client: client}), nil, TitleConfig{})

	conversation, err := service.Generate(context.Background(), "account-1", "conversation-1")
	if err != nil {
		t.Fatal(err)
	}
	if conversation.Title != "用户手动标题" || conversation.TitleStatus != "manual" {
		t.Fatalf("manual conversation = %#v", conversation)
	}
	if _, err := service.Generate(context.Background(), "account-1", "conversation-1"); err != nil {
		t.Fatal(err)
	}
	if client.calls != 1 {
		t.Fatalf("model calls = %d, want 1", client.calls)
	}
}

func TestTitleServicePersistsFallbackAfterModelContextIsCanceled(t *testing.T) {
	client := &titleTestProvider{}
	repo := newTitleTestRepository("message-user-1", "answer-1")
	service := NewTitle(repo, testGateway(fakeProviderResolver{client: client}), nil, TitleConfig{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	conversation, err := service.Generate(ctx, "account-1", "conversation-1")
	if err != nil {
		t.Fatal(err)
	}
	if conversation.TitleStatus != "fallback" || conversation.Title != "如何学习 Go Agent" {
		t.Fatalf("conversation = %#v", conversation)
	}
}

func TestSanitizeConversationTitle(t *testing.T) {
	tests := map[string]string{
		"## 标题：**消息分支设计**\n":           "消息分支设计",
		"[Go 入门](https://go.dev)\n第二行": "Go 入门 第二",
		"\"超长标题一二三四五六七八九\"":            "超长标题一二三四",
	}
	for input, expected := range tests {
		if got := sanitizeConversationTitle(input, 8); got != expected {
			t.Errorf("sanitizeConversationTitle(%q) = %q, want %q", input, got, expected)
		}
	}
}

type titleTestProvider struct {
	response provider.ChatResponse
	err      error
	calls    int
}

type titleEventRecorder struct {
	userID  string
	name    string
	payload map[string]any
}

func (r *titleEventRecorder) Publish(userID, name string, payload map[string]any) {
	r.userID, r.name, r.payload = userID, name, payload
}

func (p *titleTestProvider) Chat(ctx context.Context, _ provider.ChatRequest) (provider.ChatResponse, error) {
	p.calls++
	if err := ctx.Err(); err != nil {
		return provider.ChatResponse{}, err
	}
	return p.response, p.err
}

func (*titleTestProvider) ChatStream(context.Context, provider.ChatRequest) <-chan provider.StreamEvent {
	events := make(chan provider.StreamEvent)
	close(events)
	return events
}

type titleTestRepository struct {
	claim                repository.TitleGenerationClaim
	conversation         model.Conversation
	usage                repository.TitleGenerationUsage
	claimed              bool
	manualBeforeComplete bool
}

func newTitleTestRepository(userMessageID, answerID string) *titleTestRepository {
	replyTo := userMessageID
	return &titleTestRepository{
		claim: repository.TitleGenerationClaim{
			Conversation: model.Conversation{ID: "conversation-1", Title: "New conversation", TitleStatus: "default"},
			UserMessage:  model.Message{ID: userMessageID, Role: "user", Content: "如何学习 Go Agent？", Status: "complete"},
			Answer:       model.Message{ID: answerID, Role: "assistant", Content: "从接口和流式响应开始。", Provider: "openai", Model: "demo", Status: "complete", ReplyToMessageID: &replyTo},
		},
		conversation: model.Conversation{ID: "conversation-1", Title: "New conversation", TitleStatus: "default"},
	}
}

func (r *titleTestRepository) ClaimTitleGeneration(context.Context, string, string, time.Time) (repository.TitleGenerationClaim, bool, error) {
	if r.claimed || r.conversation.TitleStatus == "manual" || r.conversation.TitleStatus == "generated" || r.conversation.TitleStatus == "fallback" {
		return repository.TitleGenerationClaim{Conversation: r.conversation}, false, nil
	}
	r.claimed = true
	r.conversation.TitleStatus = "generating"
	r.claim.Conversation = r.conversation
	return r.claim, true, nil
}

func (r *titleTestRepository) CompleteTitleGeneration(ctx context.Context, _, _, title, status string, usage repository.TitleGenerationUsage) (model.Conversation, bool, error) {
	if err := ctx.Err(); err != nil {
		return model.Conversation{}, false, err
	}
	if r.manualBeforeComplete {
		r.conversation.Title = "用户手动标题"
		r.conversation.TitleStatus = "manual"
		return r.conversation, false, nil
	}
	r.conversation.Title = title
	r.conversation.TitleStatus = status
	r.usage = usage
	return r.conversation, true, nil
}

func (*titleTestRepository) Create(context.Context, *model.Conversation) error { return nil }
func (*titleTestRepository) ListByUserID(context.Context, string) ([]model.Conversation, error) {
	return nil, nil
}

func (*titleTestRepository) ListPageByUserID(context.Context, string, repository.ConversationPageQuery) ([]model.Conversation, error) {
	return nil, nil
}
func (*titleTestRepository) FindByIDForUser(context.Context, string, string) (model.Conversation, error) {
	return model.Conversation{}, nil
}
func (*titleTestRepository) UpdateTitle(context.Context, string, string, string) (model.Conversation, error) {
	return model.Conversation{}, nil
}
func (*titleTestRepository) Delete(context.Context, string, string) error { return nil }
func (*titleTestRepository) Touch(context.Context, string) error          { return nil }
func (*titleTestRepository) SaveSummaryVersion(context.Context, string, *model.ConversationSummaryVersion, bool) error {
	return nil
}
