package tool

import (
	"context"

	"github.com/example/agent-platform/backend/internal/domain"
)

type PlanUpdater interface {
	Update(ctx context.Context, input domain.UpdatePlanInput) (domain.Plan, error)
}
type PlanUpdateTool struct {
	toolSpec
	plans PlanUpdater
}

func NewPlanUpdate(plans PlanUpdater) *PlanUpdateTool {
	return &PlanUpdateTool{newSpec("plan", "update", "control", "保存当前会话的完整任务计划。最多 20 步，至多一个 in_progress；只是进度记录，不代替执行。", `{"type":"object","properties":{"steps":{"type":"array","minItems":1,"maxItems":20,"items":{"type":"object","properties":{"title":{"type":"string","minLength":1,"maxLength":200},"status":{"type":"string","enum":["pending","in_progress","completed"]}},"required":["title","status"],"additionalProperties":false}}},"required":["steps"],"additionalProperties":false}`, "control"), plans}
}
func (t *PlanUpdateTool) Execute(ctx context.Context, args string) (string, error) {
	return executeTyped(ctx, args, t.plans.Update)
}
