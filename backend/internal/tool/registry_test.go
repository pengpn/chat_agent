package tool

import (
	"context"
	"testing"

	"github.com/example/agent-platform/backend/internal/provider"
)

type versionedTool struct {
	*scriptedTool
	version, namespace string
	approval           bool
}

func (t versionedTool) Metadata() Metadata {
	return Metadata{Namespace: t.namespace, Name: "demo", Version: t.version, Category: "test", Source: "builtin", Exposure: "model", WireName: t.name, RequiresApproval: t.approval}
}

func TestRegistryPinsVersionAndSchemaForEntireTurn(t *testing.T) {
	ctx := context.Background()
	one := versionedTool{&scriptedTool{name: "demo", result: "v1"}, "1.0.0", "test", false}
	two := versionedTool{&scriptedTool{name: "demo", result: "v2"}, "2.0.0", "test", false}
	r := NewRegistry(one)
	set := r.SnapshotForUser(ctx, "u")
	if err := r.Register(two); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(one); err == nil {
		t.Fatal("重复注册不应该覆盖")
	}
	if err := r.Activate("builtin:test.demo", "2.0.0"); err != nil {
		t.Fatal(err)
	}
	got, err := set.Execute(ctx, "u", callFor("demo", "{}"))
	if err != nil || got != "v1" {
		t.Fatalf("旧任务版本漂移: %q %v", got, err)
	}
	got, err = r.ExecuteForUser(ctx, "u", callFor("demo", "{}"))
	if err != nil || got != "v2" {
		t.Fatalf("新任务未启用新版本: %q %v", got, err)
	}
	conflict := versionedTool{&scriptedTool{name: "demo"}, "1.0.0", "other", false}
	if err := r.Register(conflict); err == nil {
		t.Fatal("不同工具不能占用相同供应商名称")
	}
	definitions := set.Definitions()
	definitions[0].Function.Parameters[0] = '!'
	if !jsonValidDefinition(set.Definitions()[0]) {
		t.Fatal("外部修改污染快照")
	}
}
func jsonValidDefinition(d provider.ToolDefinition) bool {
	return len(d.Function.Parameters) > 0 && d.Function.Parameters[0] == '{'
}
func TestRegistryRejectsUnadvertisedToolsAndMissingApproval(t *testing.T) {
	ctx := context.Background()
	r := NewRegistry()
	set := r.SnapshotForUser(ctx, "u")
	item := versionedTool{&scriptedTool{name: "demo", result: "ran"}, "1.0.0", "test", true}
	if err := r.Register(item); err != nil {
		t.Fatal(err)
	}
	if _, err := set.Execute(ctx, "u", callFor("demo", "{}")); err == nil {
		t.Fatal("不能执行任务开始后新增的工具")
	}
	if _, err := r.ExecuteForUser(ctx, "u", callFor("demo", "{}")); err == nil || item.lastArgs != "" {
		t.Fatal("缺少审批服务必须拒绝执行")
	}
}
func TestMVPToolSchemasCompileAndSearchIsOptIn(t *testing.T) {
	deps := BuiltinDependencies{Plans: &stubPlanUpdater{}, Interactions: &stubUserInteractor{}, Artifacts: &stubArtifactStore{}}
	r := NewRegistry(NewBuiltinTools(deps)...)
	if len(r.Definitions()) != 5 {
		t.Fatal("未开启搜索时应有五个新工具")
	}
	deps.Search = &stubWebSearcher{}
	all := NewRegistry(NewBuiltinTools(deps)...)
	if len(all.Definitions()) != 6 {
		t.Fatal("开启搜索时应有六个新工具")
	}
	if _, err := all.ExecuteForUser(context.Background(), "u", callFor("user_request_approval", `{"tool":"shell_exec","arguments":{},"reason":"测试","approved":true}`)); err == nil {
		t.Fatal("模型不能传入伪造批准字段")
	}
}
