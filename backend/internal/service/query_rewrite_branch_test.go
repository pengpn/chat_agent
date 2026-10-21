package service

import (
	"context"
	"strings"
	"testing"

	"github.com/example/agent-platform/backend/internal/database/model"
)

func TestGroundingRewriteUsesSelectedBranchAndKeepsOriginalGenerationQuestion(t *testing.T) {
	u1, a1 := "root-user", "root-answer"
	u2 := "current-user"
	for _, mode := range []string{"新消息", "重新生成", "编辑消息"} {
		t.Run(mode, func(t *testing.T) {
			history := []model.Message{
				{ID: u1, Role: "user", Content: "介绍专业版", Status: "complete", IsActiveChild: true},
				{ID: a1, Role: "assistant", Content: "专业版介绍", Status: "complete", ParentMessageID: &u1, ReplyToMessageID: &u1, IsActiveChild: true, IsActiveResponse: true},
				{ID: "inactive", Role: "assistant", Content: "其他分支的内容", Status: "complete", ParentMessageID: &u1, ReplyToMessageID: &u1},
			}
			if mode != "新消息" {
				history = append(history,
					model.Message{ID: u2, Role: "user", Content: "它的退款期限？", Status: "complete", ParentMessageID: &a1, IsActiveChild: true, KnowledgeBaseIDs: model.JSONStringArray{"base-1"}},
					model.Message{ID: "old-answer", Role: "assistant", Content: "旧回答不能用于改写本轮问题", Status: "complete", ParentMessageID: &u2, ReplyToMessageID: &u2, IsActiveChild: true, IsActiveResponse: true, Provider: "openai", Model: "demo"},
				)
			}
			repo := &fakeChatRepository{conversation: model.Conversation{ID: "conversation-1"}, history: history}
			retriever := &fakeKnowledgeRetriever{result: KnowledgeRetrievalResult{Matches: groundingMatches()}}
			chat := NewChat(repo, testGateway(fakeProviderResolver{client: &groundingAnswerProvider{answer: "七天[1]"}}), nil, nil, 4, nil, "系统提示不能进入改写历史", StreamConfig{}, ContextConfig{})
			chat.ConfigureKnowledgeRetrieval(retriever)
			var session *ChatSession
			var err error
			switch mode {
			case "新消息":
				session, err = chat.Prepare(context.Background(), "user-1", "conversation-1", ChatInput{Content: "它的退款期限？", Provider: "openai", Model: "demo", KnowledgeBaseIDs: []string{"base-1"}})
			case "重新生成":
				session, err = chat.PrepareRegeneration(context.Background(), "user-1", "conversation-1", "old-answer", RegenerateInput{Provider: "openai", Model: "demo"})
			case "编辑消息":
				session, err = chat.PrepareEdit(context.Background(), "user-1", "conversation-1", u2, ChatInput{Content: "它的退款期限？", Provider: "openai", Model: "demo"})
			}
			if err != nil {
				t.Fatal(err)
			}
			got := retriever.requests[0]
			if len(got.History) != 2 || got.History[0].Content != "介绍专业版" || got.History[1].Content != "专业版介绍" {
				t.Fatalf("改写必须只使用当前消息的祖先分支: %+v", got.History)
			}
			current := session.request.Messages[len(session.request.Messages)-1]
			if session.UserMessage.Content != "它的退款期限？" || !strings.HasPrefix(current.Content, "它的退款期限？\n\nBEGIN_RETRIEVED") {
				t.Fatalf("不能替换原始生成问题: %+v", current)
			}
		})
	}
}
