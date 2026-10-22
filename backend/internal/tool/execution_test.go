package tool

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/example/agent-platform/backend/internal/config"
	"github.com/example/agent-platform/backend/internal/execution"
)

type unusedRunner struct{}

func (unusedRunner) Run(context.Context, execution.Snapshot, string) (execution.ShellResult, execution.Snapshot, error) {
	panic("文件测试不应启动 Shell")
}

func TestExecutionToolSchemaAuthorizationAndScope(t *testing.T) {
	g, err := execution.New(config.ExecutionConfig{Enabled: true, WorkspaceRoot: t.TempDir(), MaxConcurrent: 1, MaxWorkspaceBytes: 1 << 20, MaxFileBytes: 1 << 16, MaxEntries: 64, MaxOutputBytes: 4096, Timeout: time.Second}, unusedRunner{})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	registry := NewDefaultRegistry(NewBuiltinTools(BuiltinDependencies{Execution: g})...)
	registry.Configure(SafetyConfig{Policy: NewRolePolicy(stubRoleLookup{admin: false}, ExecutionToolNames())})
	definitions := registry.DefinitionsForUser(context.Background(), "user")
	if len(definitions) != 1 || definitions[0].Function.Name != "calculator" {
		t.Fatalf("非管理员不应看到执行工具: %+v", definitions)
	}
	if _, err := registry.ExecuteForConversation(context.Background(), "user", "chat", callFor("file_write", `{"path":"a","content":"text"}`)); err == nil {
		t.Fatal("执行入口也必须再次授权")
	}
	registry.Configure(SafetyConfig{Policy: AllowAllPolicy{}})
	if _, err := registry.ExecuteForUser(context.Background(), "user", callFor("file_write", `{"path":"a","content":"text"}`)); err == nil {
		t.Fatal("没有会话身份不能执行文件工具")
	}
	for _, arguments := range []string{`{"path":"a"}`, `{"path":"a","content":"x","userID":"other"}`, `null`, `{"path":"a","content":"x"} {}`, `{"path":"a","content":null}`} {
		if _, err := registry.ExecuteForConversation(context.Background(), "user", "chat", callFor("file_write", arguments)); err == nil {
			t.Fatalf("非法参数未拒绝: %s", arguments)
		}
	}
	result, err := registry.ExecuteForConversation(context.Background(), "user", "chat", callFor("file_write", `{"path":"a","content":"学习工具"}`))
	if err != nil || !json.Valid([]byte(result)) {
		t.Fatalf("必须返回完整 JSON: %s,%v", result, err)
	}
	result, err = registry.ExecuteForConversation(context.Background(), "user", "chat", callFor("file_read", `{"path":"a"}`))
	if err != nil || !json.Valid([]byte(result)) {
		t.Fatalf("文件读取失败: %s,%v", result, err)
	}
	if len(NewBuiltinTools(BuiltinDependencies{})) != 0 {
		t.Fatal("关闭执行网关时不能注册工具")
	}
}
