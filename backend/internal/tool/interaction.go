package tool

import (
	"context"
	"encoding/json"
	"time"

	"github.com/example/agent-platform/backend/internal/domain"
)

type UserInteractor interface {
	Ask(ctx context.Context, input domain.AskUserInput) (domain.UserAnswer, error)
	RequestApproval(ctx context.Context, input domain.ApprovalRequest) (domain.ApprovalAnswer, error)
}
type UserAskTool struct {
	toolSpec
	interactions UserInteractor
}

func NewUserAsk(interactions UserInteractor, timeout time.Duration) *UserAskTool {
	spec := newSpec("user", "ask", "control", "向真实用户提问并等待网页答复。不要自己虚构用户回答。可提供 2–5 个选项，用户仍可自由输入。", `{"type":"object","properties":{"question":{"type":"string","minLength":1,"maxLength":1000},"options":{"type":"array","minItems":2,"maxItems":5,"items":{"type":"string","minLength":1,"maxLength":200}}},"required":["question"],"additionalProperties":false}`, "control")
	spec.timeout = humanTimeout(timeout)
	return &UserAskTool{spec, interactions}
}
func (t *UserAskTool) Execute(ctx context.Context, args string) (string, error) {
	return executeTyped(ctx, args, t.interactions.Ask)
}

type approvalInput struct {
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
	Reason    string          `json:"reason"`
}
type UserRequestApprovalTool struct {
	toolSpec
	interactions UserInteractor
}

func NewUserRequestApproval(interactions UserInteractor, timeout time.Duration) *UserRequestApprovalTool {
	spec := newSpec("user", "request_approval", "control", "为敏感工具申请一次性执行审批。tool 填本轮工具名（例如 shell_exec），arguments 填之后实际执行的完整参数。审批绑定本轮任务、版本与参数；批准后仍须调用目标工具，参数变更需重新审批。", `{"type":"object","properties":{"tool":{"type":"string","minLength":1},"arguments":{"type":"object"},"reason":{"type":"string","minLength":1,"maxLength":1000}},"required":["tool","arguments","reason"],"additionalProperties":false}`, "control")
	spec.timeout = humanTimeout(timeout)
	return &UserRequestApprovalTool{spec, interactions}
}
func (t *UserRequestApprovalTool) Execute(ctx context.Context, args string) (string, error) {
	return executeTyped(ctx, args, func(ctx context.Context, in approvalInput) (domain.ApprovalAnswer, error) {
		// 只有适配层理解本轮工具目录；业务服务接收已解析的固定目标，不再按名称分派。
		target, err := ResolveApproval(ctx, in.Tool, string(in.Arguments))
		if err != nil {
			return domain.ApprovalAnswer{}, err
		}
		return t.interactions.RequestApproval(ctx, domain.ApprovalRequest{Target: target, Arguments: in.Arguments, Reason: in.Reason})
	})
}
func humanTimeout(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return 90 * time.Second
	}
	return timeout
}
