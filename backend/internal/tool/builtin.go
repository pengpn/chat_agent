package tool

import (
	"time"

	"github.com/example/agent-platform/backend/internal/execution"
	"github.com/example/agent-platform/backend/internal/websearch"
)

// BuiltinDependencies 按能力注入依赖，不把一个万能后端交给所有工具。
type BuiltinDependencies struct {
	Execution    *execution.Gateway
	Plans        PlanUpdater
	Interactions UserInteractor
	Artifacts    ArtifactStore
	Search       websearch.Searcher
	HumanTimeout time.Duration
}

// NewBuiltinTools 只负责装配；注册、版本选择和执行策略仍由同一个 Registry 处理。
func NewBuiltinTools(deps BuiltinDependencies) []Tool {
	items := []Tool{}
	if deps.Execution != nil {
		items = append(items, NewShellExec(deps.Execution), NewFileList(deps.Execution), NewFileRead(deps.Execution), NewFileWrite(deps.Execution), NewFileApplyPatch(deps.Execution), NewFileSearch(deps.Execution))
	}
	if deps.Plans != nil {
		items = append(items, NewPlanUpdate(deps.Plans))
	}
	if deps.Interactions != nil {
		items = append(items, NewUserAsk(deps.Interactions, deps.HumanTimeout), NewUserRequestApproval(deps.Interactions, deps.HumanTimeout))
	}
	if deps.Artifacts != nil {
		items = append(items, NewArtifactSave(deps.Artifacts), NewArtifactRead(deps.Artifacts))
	}
	if deps.Search != nil {
		items = append(items, NewWebSearch(deps.Search))
	}
	return items
}
func ExecutionToolNames() []string {
	return []string{"shell_exec", "file_list", "file_read", "file_write", "file_apply_patch", "file_search"}
}
