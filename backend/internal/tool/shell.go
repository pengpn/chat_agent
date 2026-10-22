package tool

import (
	"context"

	"github.com/example/agent-platform/backend/internal/execution"
)

type ShellRunner interface {
	Shell(ctx context.Context, command string) (execution.ShellResult, error)
}
type ShellExecTool struct {
	toolSpec
	runner ShellRunner
}

func NewShellExec(runner ShellRunner) *ShellExecTool {
	spec := newSpec("shell", "exec", "shell", "在当前会话隔离的 Docker 工作区 /workspace 中执行 Shell。禁止网络，宿主目录和密钥不可见。时长由服务端限制，不支持后台交互。返回退出码和截断标记。", `{"type":"object","properties":{"command":{"type":"string","minLength":1,"maxLength":16384}},"required":["command"],"additionalProperties":false}`, "execute")
	spec.metadata.RequiresApproval = true
	return &ShellExecTool{spec, runner}
}
func (t *ShellExecTool) Execute(ctx context.Context, args string) (string, error) {
	return executeTyped(ctx, args, func(ctx context.Context, in struct {
		Command string `json:"command"`
	}) (execution.ShellResult, error) {
		return t.runner.Shell(ctx, in.Command)
	})
}
