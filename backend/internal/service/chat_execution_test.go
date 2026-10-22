package service

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/example/agent-platform/backend/internal/config"
	"github.com/example/agent-platform/backend/internal/database/model"
	"github.com/example/agent-platform/backend/internal/execution"
	"github.com/example/agent-platform/backend/internal/provider"
	"github.com/example/agent-platform/backend/internal/tool"
	"github.com/google/uuid"
)

type fileCallingProvider struct{ calls int }

type fileOnlyRunner struct{}

func (fileOnlyRunner) Run(context.Context, execution.Snapshot, string) (execution.ShellResult, execution.Snapshot, error) {
	panic("文件工具测试不应启动 Docker")
}

type questionCallingProvider struct{ calls int }

func (p *questionCallingProvider) Chat(_ context.Context, request provider.ChatRequest) (provider.ChatResponse, error) {
	p.calls++
	if p.calls == 1 {
		return provider.ChatResponse{ToolCalls: []provider.ToolCall{{ID: "ask-call", Type: "function", Function: provider.ToolFunctionCall{Name: "user_ask", Arguments: `{"question":"你希望整理哪个主题？"}`}}}}, nil
	}
	return provider.ChatResponse{Content: request.Messages[len(request.Messages)-1].Content}, nil
}
func (*questionCallingProvider) ChatStream(context.Context, provider.ChatRequest) <-chan provider.StreamEvent {
	panic("本测试仅调用同步接口")
}

func TestChatHumanToolWorksWithoutClientIdempotencyHeader(t *testing.T) {
	repo := newAgentMemoryRepo()
	human := NewInteraction(repo, agentOwnedConversation{}, nil, 2*time.Second)
	client := &questionCallingProvider{}
	chat := NewChat(&fakeChatRepository{}, testGateway(fakeProviderResolver{client: client}), tool.NewRegistry(tool.NewBuiltinTools(tool.BuiltinDependencies{Interactions: human, HumanTimeout: 2 * time.Second})...), nil, 4, nil, "", StreamConfig{}, ContextConfig{})
	conversationID := uuid.NewString()
	session, err := chat.Prepare(context.Background(), "user", conversationID, ChatInput{Provider: "openai", Model: "demo", Content: "请先问我"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		result, err := chat.Chat(context.Background(), session)
		if err == nil && !strings.Contains(result.Content, "Golang 工具调用") {
			err = fmt.Errorf("未使用真实用户答复: %s", result.Content)
		}
		done <- err
	}()
	var item model.ToolInteraction
	select {
	case item = <-repo.created:
	case err := <-done:
		t.Fatalf("创建用户交互前结束: %v", err)
	case <-time.After(time.Second):
		t.Fatal("未创建用户交互")
	}
	if len(item.OperationID) != 64 {
		t.Fatalf("没有服务端任务指纹: %q", item.OperationID)
	}
	if err := human.Respond(context.Background(), "user", conversationID, item.ID, ToolUserResponse{Answer: "Golang 工具调用"}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func (p *fileCallingProvider) Chat(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
	p.calls++
	if p.calls == 1 {
		return provider.ChatResponse{ToolCalls: []provider.ToolCall{{ID: "file-call", Type: "function", Function: provider.ToolFunctionCall{Name: "file_write", Arguments: `{"path":"note.txt","content":"来自会话的文件"}`}}}}, nil
	}
	return provider.ChatResponse{Content: "已创建文件。"}, nil
}
func (*fileCallingProvider) ChatStream(context.Context, provider.ChatRequest) <-chan provider.StreamEvent {
	panic("本测试不调用流式接口")
}

func TestChatToolCallingPassesServerOwnedConversationScope(t *testing.T) {
	g, err := execution.New(config.ExecutionConfig{Enabled: true, WorkspaceRoot: t.TempDir(), MaxConcurrent: 1, MaxWorkspaceBytes: 1 << 20, MaxFileBytes: 1 << 16, MaxEntries: 64, MaxOutputBytes: 4096, Timeout: time.Second}, fileOnlyRunner{})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	client := &fileCallingProvider{}
	chat := NewChat(&fakeChatRepository{}, testGateway(fakeProviderResolver{client: client}), tool.NewDefaultRegistry(tool.NewBuiltinTools(tool.BuiltinDependencies{Execution: g})...), nil, 4, nil, "", StreamConfig{}, ContextConfig{})
	session, err := chat.Prepare(context.Background(), "user-a", "chat-a", ChatInput{Provider: "openai", Model: "demo", Content: "创建笔记"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chat.Chat(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	result, err := g.Read(execution.WithScope(context.Background(), execution.Scope{UserID: "user-a", ConversationID: "chat-a"}), "note.txt", 0, 0)
	if err != nil || result.Content != "来自会话的文件" {
		t.Fatalf("业务层没有传递正确身份: %+v,%v", result, err)
	}
}
