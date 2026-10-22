package service

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/example/agent-platform/backend/internal/database/model"
	"github.com/example/agent-platform/backend/internal/domain"
	"github.com/example/agent-platform/backend/internal/execution"
	"github.com/example/agent-platform/backend/internal/provider"
	"github.com/example/agent-platform/backend/internal/repository"
	"github.com/example/agent-platform/backend/internal/tool"
	"github.com/google/uuid"
)

// 未覆盖的方法保留嵌入接口：误调用会直接失败，避免假的仓储悄悄放过测试。
type agentMemoryRepo struct {
	repository.AgentToolRepository
	mu        sync.Mutex
	items     map[string]model.ToolInteraction
	artifacts map[string]model.AgentArtifact
	plan      *model.AgentPlan
	created   chan model.ToolInteraction
}

func newAgentMemoryRepo() *agentMemoryRepo {
	return &agentMemoryRepo{items: map[string]model.ToolInteraction{}, artifacts: map[string]model.AgentArtifact{}, created: make(chan model.ToolInteraction, 10)}
}
func (r *agentMemoryRepo) CreateInteraction(_ context.Context, i *model.ToolInteraction) error {
	r.mu.Lock()
	i.ID = uuid.NewString()
	r.items[i.ID] = *i
	r.mu.Unlock()
	r.created <- *i
	return nil
}
func (r *agentMemoryRepo) GetInteraction(_ context.Context, u, c, id string) (model.ToolInteraction, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	i, ok := r.items[id]
	if !ok || i.UserID != u || i.ConversationID != c {
		return i, domain.ErrNotFound
	}
	return i, nil
}
func (r *agentMemoryRepo) Respond(_ context.Context, u, c, id string, raw json.RawMessage) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	i, ok := r.items[id]
	if !ok || i.UserID != u || i.ConversationID != c || i.Status != "pending" || time.Now().After(i.ExpiresAt) {
		return repository.ErrInteractionClosed
	}
	i.Status = "responded"
	i.Response = raw
	r.items[id] = i
	return nil
}
func (r *agentMemoryRepo) CancelInteraction(_ context.Context, u, c, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	i := r.items[id]
	i.Status = "canceled"
	r.items[id] = i
	return nil
}
func (r *agentMemoryRepo) ConsumeApproval(_ context.Context, u, c, operation, key string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, i := range r.items {
		if i.UserID == u && i.ConversationID == c && i.OperationID == operation && i.ApprovalKey == key && i.Kind == "approval" && i.Status == "responded" && time.Now().Before(i.ExpiresAt) && strings.Contains(string(i.Response), `"approved":true`) {
			i.Status = "consumed"
			r.items[id] = i
			return true, nil
		}
	}
	return false, nil
}
func (r *agentMemoryRepo) SaveArtifact(_ context.Context, a *model.AgentArtifact) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	a.ID = uuid.NewString()
	r.artifacts[a.ID] = *a
	return nil
}
func (r *agentMemoryRepo) GetArtifact(_ context.Context, u, c, id string) (model.AgentArtifact, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.artifacts[id]
	if !ok || a.UserID != u || a.ConversationID != c {
		return a, domain.ErrNotFound
	}
	return a, nil
}
func (r *agentMemoryRepo) SavePlan(_ context.Context, p *model.AgentPlan) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	v := *p
	if r.plan != nil {
		v.Revision = r.plan.Revision + 1
	}
	r.plan = &v
	return nil
}
func (r *agentMemoryRepo) GetPlan(context.Context, string, string) (*model.AgentPlan, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.plan, nil
}

type agentOwnedConversation struct {
	repository.ConversationRepository
}

func (agentOwnedConversation) FindByIDForUser(_ context.Context, u, c string) (model.Conversation, error) {
	if u != "user" {
		return model.Conversation{}, domain.ErrNotFound
	}
	return model.Conversation{ID: c, UserID: u}, nil
}
func agentTestScope() context.Context {
	return execution.WithScope(context.Background(), execution.Scope{UserID: "user", ConversationID: uuid.NewString(), OperationID: "run-1"})
}
func agentCall(name, args string) provider.ToolCall {
	return provider.ToolCall{ID: uuid.NewString(), Function: provider.ToolFunctionCall{Name: name, Arguments: args}}
}

type approvalShell struct{ ran int }

func (s *approvalShell) Metadata() tool.Metadata {
	return tool.Metadata{Namespace: "shell", Name: "exec", Version: "1.0.0", Source: "builtin", Category: "execution", Exposure: "model", WireName: "shell_exec", RequiresApproval: true}
}
func (s *approvalShell) Definition() provider.ToolDefinition {
	return provider.ToolDefinition{Type: "function", Function: provider.ToolFunctionDefinition{Name: "shell_exec", Parameters: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}},"required":["command"],"additionalProperties":false}`)}}
}
func (s *approvalShell) Execute(context.Context, string) (string, error) {
	s.ran++
	return `{"exitCode":0}`, nil
}

func TestApprovalRequiresRealUserAndIsBoundedAndSingleUse(t *testing.T) {
	r := newAgentMemoryRepo()
	s := NewInteraction(r, agentOwnedConversation{}, nil, time.Second)
	shell := &approvalShell{}
	registry := tool.NewRegistry(append([]tool.Tool{shell}, tool.NewBuiltinTools(tool.BuiltinDependencies{Interactions: s, HumanTimeout: time.Second})...)...)
	registry.SetApprovalGuard(s)
	ctx := agentTestScope()
	scope, _ := execution.ScopeFromContext(ctx)
	set := registry.SnapshotForUser(ctx, "user")
	if _, err := set.Execute(ctx, "user", agentCall("shell_exec", `{"command":"echo hi"}`)); err == nil {
		t.Fatal("未审批执行")
	}
	done := make(chan error, 1)
	go func() {
		_, err := set.Execute(ctx, "user", agentCall("user_request_approval", `{"tool":"shell_exec","arguments":{"command":"echo hi"},"reason":"验证沙箱"}`))
		done <- err
	}()
	var item model.ToolInteraction
	select {
	case item = <-r.created:
	case <-time.After(time.Second):
		t.Fatal("没有创建审批")
	}
	approved := true
	if err := s.Respond(ctx, "attacker", scope.ConversationID, item.ID, ToolUserResponse{Approved: &approved}); err == nil {
		t.Fatal("跨用户审批")
	}
	if err := s.Respond(ctx, "user", scope.ConversationID, item.ID, ToolUserResponse{Answer: "approved"}); err == nil {
		t.Fatal("文本不能代替批准字段")
	}
	if err := s.Respond(ctx, "user", scope.ConversationID, item.ID, ToolUserResponse{Approved: &approved}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := set.Execute(ctx, "user", agentCall("shell_exec", `{"command":"other"}`)); err == nil {
		t.Fatal("审批不能更改命令")
	}
	other := execution.WithScope(ctx, execution.Scope{UserID: "user", ConversationID: scope.ConversationID, OperationID: "run-2"})
	if _, err := set.Execute(other, "user", agentCall("shell_exec", `{"command":"echo hi"}`)); err == nil {
		t.Fatal("不能跨任务使用审批")
	}
	if _, err := set.Execute(ctx, "user", agentCall("shell_exec", `{ "command" : "echo hi" }`)); err != nil {
		t.Fatal(err)
	}
	if _, err := set.Execute(ctx, "user", agentCall("shell_exec", `{"command":"echo hi"}`)); err == nil {
		t.Fatal("审批不能重复消费")
	}
	if shell.ran != 1 {
		t.Fatalf("执行次数 %d", shell.ran)
	}
}

func TestUserAskWaitsForUserAndCancellationClosesPending(t *testing.T) {
	r := newAgentMemoryRepo()
	s := NewInteraction(r, agentOwnedConversation{}, nil, time.Second)
	ctx, cancel := context.WithCancel(agentTestScope())
	done := make(chan error, 1)
	go func() { _, err := s.Ask(ctx, domain.AskUserInput{Question: "选哪个？"}); done <- err }()
	item := <-r.created
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("取消未传播: %v", err)
	}
	i, _ := r.GetInteraction(context.Background(), item.UserID, item.ConversationID, item.ID)
	if i.Status != "canceled" {
		t.Fatalf("交互仍在等待: %s", i.Status)
	}
	approved := true
	if err := s.Respond(context.Background(), item.UserID, item.ConversationID, item.ID, ToolUserResponse{Approved: &approved}); err == nil {
		t.Fatal("问题不能作为审批")
	}
}

func TestArtifactRoundTripAndPlanValidation(t *testing.T) {
	r := newAgentMemoryRepo()
	artifacts := NewArtifact(r, agentOwnedConversation{}, nil)
	plans := NewPlan(r, agentOwnedConversation{}, nil)
	ctx := agentTestScope()
	saved, err := artifacts.Save(ctx, domain.SaveArtifactInput{Title: "学习笔记", MediaType: "text/markdown", Content: "你好 Markdown"})
	if err != nil {
		t.Fatal(err)
	}
	read, err := artifacts.Read(ctx, domain.ReadArtifactInput{ArtifactID: saved.ArtifactID, Limit: 4})
	if err != nil || read.Content != "你" || read.NextOffset != 3 {
		t.Fatalf("UTF-8 分页错误: %+v %v", read, err)
	}
	if _, err := artifacts.Save(ctx, domain.SaveArtifactInput{Title: "bad", MediaType: "application/json", Content: "not json"}); err == nil {
		t.Fatal("非法 JSON 不应保存")
	}
	if _, err := plans.Update(ctx, domain.UpdatePlanInput{Steps: []domain.PlanStep{{Title: "a", Status: "in_progress"}, {Title: "b", Status: "in_progress"}}}); err == nil {
		t.Fatal("不能同时进行两个步骤")
	}
	for range 2 {
		if _, err := plans.Update(ctx, domain.UpdatePlanInput{Steps: []domain.PlanStep{{Title: "a", Status: "completed"}}}); err != nil {
			t.Fatal(err)
		}
	}
	if r.plan.Revision != 2 {
		t.Fatal("计划未记录修订版本")
	}
}
