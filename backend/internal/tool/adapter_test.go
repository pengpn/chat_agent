package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/example/agent-platform/backend/internal/domain"
	"github.com/example/agent-platform/backend/internal/execution"
	"github.com/example/agent-platform/backend/internal/websearch"
)

type stubPlanUpdater struct{ input domain.UpdatePlanInput }

func (s *stubPlanUpdater) Update(_ context.Context, in domain.UpdatePlanInput) (domain.Plan, error) {
	s.input = in
	return domain.Plan{Steps: in.Steps, Revision: 1}, nil
}

type stubUserInteractor struct {
	question domain.AskUserInput
	approval domain.ApprovalRequest
	deadline time.Time
}

func (s *stubUserInteractor) Ask(ctx context.Context, in domain.AskUserInput) (domain.UserAnswer, error) {
	s.question = in
	s.deadline, _ = ctx.Deadline()
	return domain.UserAnswer{Answer: "真实答复"}, nil
}
func (s *stubUserInteractor) RequestApproval(_ context.Context, in domain.ApprovalRequest) (domain.ApprovalAnswer, error) {
	s.approval = in
	return domain.ApprovalAnswer{Approved: true}, nil
}

type stubArtifactStore struct {
	saved domain.SaveArtifactInput
	read  domain.ReadArtifactInput
}

func (s *stubArtifactStore) Save(_ context.Context, in domain.SaveArtifactInput) (domain.SavedArtifact, error) {
	s.saved = in
	return domain.SavedArtifact{ArtifactID: "id", Title: in.Title, Size: len(in.Content)}, nil
}
func (s *stubArtifactStore) Read(_ context.Context, in domain.ReadArtifactInput) (domain.ArtifactPage, error) {
	s.read = in
	return domain.ArtifactPage{ArtifactID: in.ArtifactID, Content: "内容", NextOffset: 6}, nil
}

type stubWebSearcher struct{ query string }

func (s *stubWebSearcher) Search(_ context.Context, query string) ([]websearch.Result, error) {
	s.query = query
	return []websearch.Result{{Title: "官方资料", URL: "https://example.com", Content: "片段"}}, nil
}

type stubFileOperations struct {
	called, path, content, expected string
	offset, limit                   int
	edits                           []execution.Edit
	regex                           bool
}

func (s *stubFileOperations) List(_ context.Context, path string, offset, limit int) (execution.ListResult, error) {
	s.called = "list"
	s.path = path
	s.offset = offset
	s.limit = limit
	return execution.ListResult{}, nil
}
func (s *stubFileOperations) Read(_ context.Context, path string, offset, limit int) (execution.ReadResult, error) {
	s.called = "read"
	s.path = path
	s.offset = offset
	s.limit = limit
	return execution.ReadResult{}, nil
}
func (s *stubFileOperations) Write(_ context.Context, path, content, expected string) (execution.WriteResult, error) {
	s.called = "write"
	s.path = path
	s.content = content
	s.expected = expected
	return execution.WriteResult{}, nil
}
func (s *stubFileOperations) ApplyPatch(_ context.Context, path, expected string, edits []execution.Edit) (execution.WriteResult, error) {
	s.called = "patch"
	s.path = path
	s.expected = expected
	s.edits = edits
	return execution.WriteResult{}, nil
}
func (s *stubFileOperations) Search(_ context.Context, path, pattern string, regex bool, limit int) (execution.SearchResult, error) {
	s.called = "search"
	s.path = path
	s.content = pattern
	s.regex = regex
	s.limit = limit
	return execution.SearchResult{}, nil
}

type stubShellRunner struct{ command string }

func (s *stubShellRunner) Shell(_ context.Context, command string) (execution.ShellResult, error) {
	s.command = command
	return execution.ShellResult{ExitCode: 0}, nil
}

type allowApproval struct{}

func (allowApproval) Authorize(context.Context, Descriptor, string) error { return nil }

func TestConcreteFileAdaptersMapTypedArguments(t *testing.T) {
	files := &stubFileOperations{}
	r := NewRegistry(NewFileList(files), NewFileRead(files), NewFileWrite(files), NewFileApplyPatch(files), NewFileSearch(files))
	for _, test := range []struct{ name, args, method string }{
		{"file_list", `{"path":"src","offset":2,"limit":5}`, "list"},
		{"file_read", `{"path":"src","offset":2,"limit":5}`, "read"},
		{"file_write", `{"path":"src","content":"正文","expectedSha256":"old"}`, "write"},
		{"file_apply_patch", `{"path":"src","expectedSha256":"` + strings.Repeat("a", 64) + `","edits":[{"oldText":"old","newText":"new"}]}`, "patch"},
		{"file_search", `{"path":"src","pattern":"main.*","regex":true,"limit":7}`, "search"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := r.ExecuteForUser(context.Background(), "u", callFor(test.name, test.args))
			if err != nil || !json.Valid([]byte(result)) || files.called != test.method || files.path != "src" {
				t.Fatalf("适配失败: %s %v %+v", result, err, files)
			}
		})
	}
	if files.offset != 2 || files.content != "main.*" || !files.regex || files.limit != 7 || len(files.edits) != 1 || files.edits[0].NewText != "new" {
		t.Fatalf("参数映射有损: %+v", files)
	}
}
func TestConcreteBusinessAdaptersPreserveWireResults(t *testing.T) {
	plans := &stubPlanUpdater{}
	human := &stubUserInteractor{}
	artifacts := &stubArtifactStore{}
	search := &stubWebSearcher{}
	shell := &stubShellRunner{}
	items := NewBuiltinTools(BuiltinDependencies{Plans: plans, Interactions: human, Artifacts: artifacts, Search: search, HumanTimeout: time.Minute})
	r := NewRegistry(append(items, NewShellExec(shell))...)
	r.SetApprovalGuard(allowApproval{})
	ctx := context.Background()
	for _, test := range []struct{ name, args, key string }{
		{"plan_update", `{"steps":[{"title":"整理","status":"in_progress"}]}`, "revision"},
		{"user_ask", `{"question":"读者是谁？","options":["管理者","开发者"]}`, "answer"},
		{"user_request_approval", `{"tool":"shell_exec","arguments":{"command":"echo hello"},"reason":"测试"}`, "approved"},
		{"artifact_save", `{"title":"笔记","mediaType":"text/plain","content":"正文"}`, "artifactId"},
		{"artifact_read", `{"artifactId":"00000000-0000-0000-0000-000000000001","offset":3,"limit":5}`, "nextOffset"},
		{"web_search", `{"query":"Go 官方文档"}`, "untrusted"},
		{"shell_exec", `{"command":"echo hello"}`, "exitCode"},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, err := r.ExecuteForUser(ctx, "u", callFor(test.name, test.args))
			if err != nil {
				t.Fatal(err)
			}
			var result map[string]json.RawMessage
			if err := json.Unmarshal([]byte(raw), &result); err != nil {
				t.Fatal(err)
			}
			if _, ok := result[test.key]; !ok {
				t.Fatalf("响应协议变化: %s", raw)
			}
		})
	}
	if plans.input.Steps[0].Title != "整理" || human.question.Question != "读者是谁？" || human.approval.Target.ID() != "builtin:shell.exec" || artifacts.saved.Content != "正文" || artifacts.read.Offset != 3 || search.query != "Go 官方文档" || shell.command != "echo hello" {
		t.Fatal("类型化调用丢失参数")
	}
	if time.Until(human.deadline) < 50*time.Second {
		t.Fatal("人工交互专用超时丢失")
	}
}
func TestRegistrySchemaRejectsBeforeCallingTypedService(t *testing.T) {
	plans := &stubPlanUpdater{}
	r := NewRegistry(NewPlanUpdate(plans))
	for _, args := range []string{`{}`, `{"steps":null}`, `{"steps":[{"title":"x","status":"unknown"}]}`, `{"steps":[],"userID":"forged"}`} {
		if _, err := r.ExecuteForUser(context.Background(), "u", callFor("plan_update", args)); err == nil {
			t.Fatalf("非法参数未拦截: %s", args)
		}
	}
	if plans.input.Steps != nil {
		t.Fatal("校验失败时仍调用了业务服务")
	}
}
func TestToolDescriptorJSONRemainsCompatible(t *testing.T) {
	d := Descriptor{ToolMetadata: NewShellExec(&stubShellRunner{}).Metadata(), SchemaHash: "hash"}
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "ToolMetadata") || !strings.Contains(string(raw), `"namespace":"shell"`) || !strings.Contains(string(raw), `"schemaHash":"hash"`) {
		t.Fatalf("审批元数据不兼容: %s", raw)
	}
	const previous = `{"namespace":"shell","name":"exec","version":"1.0.0","category":"shell","source":"builtin","exposure":"model","capabilities":["execute"],"wireName":"shell_exec","requiresApproval":true,"schemaHash":"hash"}`
	if string(raw) != previous {
		t.Fatalf("元数据编码变化会影响既有审批指纹: %s", raw)
	}
}
