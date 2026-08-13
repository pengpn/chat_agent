package service

import (
	"context"
	"errors"
	"testing"

	"github.com/example/agent-platform/backend/internal/domain"
	"github.com/example/agent-platform/backend/internal/moderation"
	"github.com/example/agent-platform/backend/internal/provider"
)

// scriptedAnswerProvider 返回一次固定回答；streamErr 非空时流直接失败。
type scriptedAnswerProvider struct {
	answer    string
	usage     *provider.TokenUsage
	streamErr error
}

func (p *scriptedAnswerProvider) Chat(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
	if p.streamErr != nil {
		return provider.ChatResponse{}, p.streamErr
	}
	return provider.ChatResponse{Content: p.answer, Usage: p.usage}, nil
}

func (p *scriptedAnswerProvider) ChatStream(context.Context, provider.ChatRequest) <-chan provider.StreamEvent {
	events := make(chan provider.StreamEvent, 4)
	if p.streamErr != nil {
		events <- provider.StreamEvent{Err: p.streamErr}
		close(events)
		return events
	}
	if p.answer != "" {
		events <- provider.StreamEvent{Delta: p.answer}
	}
	if p.usage != nil {
		events <- provider.StreamEvent{Usage: p.usage}
	}
	events <- provider.StreamEvent{Done: true}
	close(events)
	return events
}

func newSafetyChat(repo *fakeChatRepository, modelClient provider.Provider) *ChatService {
	return NewChat(repo, testGateway(fakeProviderResolver{client: modelClient}), nil, nil, 4, nil, "", StreamConfig{}, ContextConfig{})
}

func TestPrepareBlocksModeratedInput(t *testing.T) {
	chat := newSafetyChat(&fakeChatRepository{}, &scriptedAnswerProvider{answer: "ok"})
	chat.ConfigureSafety(SafetyDependencies{Moderator: moderation.New(true, []string{"违禁词"}, nil)})

	_, err := chat.Prepare(context.Background(), "user-1", "conversation-1", ChatInput{
		Content: "这里包含违禁词", Provider: "openai", Model: "demo",
	})
	if !errors.Is(err, ErrContentBlockedByModeration) {
		t.Fatalf("err = %v", err)
	}
}

func TestPrepareRejectsWhenQuotaExhausted(t *testing.T) {
	repo := &fakeChatRepository{}
	chat := newSafetyChat(repo, &scriptedAnswerProvider{answer: "ok"})
	repo2 := &fakeQuotaRepository{forceError: domain.ErrDailyQuotaExceeded}
	chat.ConfigureSafety(SafetyDependencies{Quotas: newTestQuota(repo2, true)})

	_, err := chat.Prepare(context.Background(), "user-1", "conversation-1", ChatInput{
		Content: "你好", Provider: "openai", Model: "demo",
	})
	if !errors.Is(err, ErrDailyQuotaExceeded) {
		t.Fatalf("err = %v", err)
	}
	if repo.lastCompletion.Status != "error" || repo.lastCompletion.FinishReason != "quota_exceeded" {
		t.Fatalf("last completion = %#v", repo.lastCompletion)
	}
}

func TestChatSettlesQuotaWithActualUsage(t *testing.T) {
	quotaRepo := &fakeQuotaRepository{}
	chat := newSafetyChat(&fakeChatRepository{}, &scriptedAnswerProvider{
		answer: "4", usage: &provider.TokenUsage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5},
	})
	chat.ConfigureSafety(SafetyDependencies{Quotas: newTestQuota(quotaRepo, true)})

	session, err := chat.Prepare(context.Background(), "user-1", "conversation-1", ChatInput{
		Content: "2+2", Provider: "openai", Model: "demo",
	})
	if err != nil {
		t.Fatal(err)
	}
	reserved := session.quotaReservedTokens
	if reserved <= 0 || len(quotaRepo.reserveCalls) != 1 {
		t.Fatalf("reserved = %d, reserve calls = %#v", reserved, quotaRepo.reserveCalls)
	}
	completed, err := chat.Chat(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != "complete" {
		t.Fatalf("status = %q", completed.Status)
	}
	if len(quotaRepo.settleCalls) != 1 || quotaRepo.settleCalls[0].first != reserved || quotaRepo.settleCalls[0].second != 5 {
		t.Fatalf("settle calls = %#v, reserved = %d", quotaRepo.settleCalls, reserved)
	}
	if quotaRepo.dailyUsed != 5 {
		t.Fatalf("daily used = %d, want actual 5", quotaRepo.dailyUsed)
	}
}

func TestStreamReleasesQuotaWhenNoUsageReported(t *testing.T) {
	quotaRepo := &fakeQuotaRepository{}
	chat := newSafetyChat(&fakeChatRepository{}, &scriptedAnswerProvider{streamErr: errors.New("upstream failure")})
	chat.ConfigureSafety(SafetyDependencies{Quotas: newTestQuota(quotaRepo, true)})

	session, err := chat.Prepare(context.Background(), "user-1", "conversation-1", ChatInput{
		Content: "你好", Provider: "openai", Model: "demo",
	})
	if err != nil {
		t.Fatal(err)
	}
	reserved := session.quotaReservedTokens
	_, status, err := chat.Stream(context.Background(), session, func(provider.StreamEvent) {})
	if err != nil {
		t.Fatal(err)
	}
	if status == "complete" {
		t.Fatalf("status = %q", status)
	}
	if len(quotaRepo.releaseCalls) != 1 || quotaRepo.releaseCalls[0].first != reserved {
		t.Fatalf("release calls = %#v, reserved = %d", quotaRepo.releaseCalls, reserved)
	}
	if quotaRepo.dailyUsed != 0 {
		t.Fatalf("daily used after compensation = %d, want 0", quotaRepo.dailyUsed)
	}
}

func TestStreamReplacesModeratedOutput(t *testing.T) {
	chat := newSafetyChat(&fakeChatRepository{}, &scriptedAnswerProvider{
		answer: "回答里含有输出违禁内容",
		usage:  &provider.TokenUsage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
	})
	chat.ConfigureSafety(SafetyDependencies{Moderator: moderation.New(true, nil, []string{"输出违禁"})})

	session, err := chat.Prepare(context.Background(), "user-1", "conversation-1", ChatInput{
		Content: "请回答", Provider: "openai", Model: "demo",
	})
	if err != nil {
		t.Fatal(err)
	}
	completed, status, err := chat.Stream(context.Background(), session, func(provider.StreamEvent) {})
	if err != nil {
		t.Fatal(err)
	}
	if status != "complete" || completed.Content != moderation.BlockedReply || completed.FinishReason != "moderation_blocked" {
		t.Fatalf("status=%q content=%q reason=%q", status, completed.Content, completed.FinishReason)
	}
}

func TestPrepareEnforcesConfigurableMessageLimit(t *testing.T) {
	chat := newSafetyChat(&fakeChatRepository{}, &scriptedAnswerProvider{answer: "ok"})
	chat.ConfigureSafety(SafetyDependencies{MaxMessageChars: 4})

	_, err := chat.Prepare(context.Background(), "user-1", "conversation-1", ChatInput{
		Content: "超过四个字符的消息", Provider: "openai", Model: "demo",
	})
	if !errors.Is(err, ErrInvalidMessageLength) {
		t.Fatalf("err = %v", err)
	}
}
