package execution

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/example/agent-platform/backend/internal/config"
)

type runnerFunc func(context.Context, Snapshot, string) (ShellResult, Snapshot, error)

func (f runnerFunc) Run(ctx context.Context, s Snapshot, command string) (ShellResult, Snapshot, error) {
	return f(ctx, s, command)
}

func testConfig(t *testing.T) config.ExecutionConfig {
	t.Helper()
	return config.ExecutionConfig{Enabled: true, WorkspaceRoot: t.TempDir(), DockerBinary: "docker", Image: "chat-agent-sandbox:local", Timeout: 20 * time.Second,
		MemoryMB: 256, CPUs: 1, PIDs: 64, MaxConcurrent: 2, MaxWorkspaceBytes: 1 << 20, MaxFileBytes: 1 << 16, MaxEntries: 64, MaxOutputBytes: 4096}
}

func testGateway(t *testing.T, runner Runner) *Gateway {
	t.Helper()
	if runner == nil {
		runner = runnerFunc(func(_ context.Context, s Snapshot, _ string) (ShellResult, Snapshot, error) {
			return ShellResult{}, s, nil
		})
	}
	g, err := New(testConfig(t), runner)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	return g
}

func scopeContext(user, conversation string) context.Context {
	return WithScope(context.Background(), Scope{UserID: user, ConversationID: conversation})
}

func TestFilesAreScopedAndRequireCurrentVersion(t *testing.T) {
	g := testGateway(t, nil)
	ctx := scopeContext("user-a", "chat-a")
	created, err := g.Write(ctx, "docs/学习.txt", "你好，Agent\n第二行", "")
	if err != nil || !created.Created {
		t.Fatalf("创建失败: %+v, %v", created, err)
	}
	first, err := g.Read(ctx, "docs/学习.txt", 0, 4)
	if err != nil || first.Content != "你" || first.NextOffset != 3 || first.SHA256 != created.SHA256 {
		t.Fatalf("UTF-8 分段错误: %+v,%v", first, err)
	}
	if _, err := g.Read(ctx, "docs/学习.txt", 1, 4); err == nil {
		t.Fatal("不能从字符中间开始读取")
	}
	for _, other := range []context.Context{scopeContext("user-b", "chat-a"), scopeContext("user-a", "chat-b")} {
		if _, err := g.Read(other, "docs/学习.txt", 0, 0); err == nil {
			t.Fatal("不能跨用户或会话读取")
		}
	}
	if _, err := g.Write(ctx, "docs/学习.txt", "误覆盖", ""); err == nil {
		t.Fatal("禁止盲目覆盖")
	}
	patched, err := g.ApplyPatch(ctx, "docs/学习.txt", created.SHA256, []Edit{{OldText: "Agent", NewText: "工具"}})
	if err != nil || patched.SHA256 == created.SHA256 {
		t.Fatalf("增量修改失败: %+v,%v", patched, err)
	}
	if _, err := g.Write(ctx, "docs/学习.txt", "过期写入", created.SHA256); err == nil {
		t.Fatal("必须拒绝过期版本")
	}
	if _, err := g.ApplyPatch(ctx, "docs/学习.txt", patched.SHA256, []Edit{{OldText: "第二行", NewText: "已改"}, {OldText: "不存在", NewText: "失败"}}); err == nil {
		t.Fatal("部分替换失败时必须整体拒绝")
	}
	current, _ := g.Read(ctx, "docs/学习.txt", 0, 0)
	if strings.Contains(current.Content, "已改") {
		t.Fatal("失败的补丁不应有部分写入")
	}
	matches, err := g.Search(ctx, "docs", "工具", false, 1)
	if err != nil || len(matches.Matches) != 1 || matches.Matches[0].Line != 1 {
		t.Fatalf("搜索结果错误: %+v,%v", matches, err)
	}
	list, err := g.List(ctx, ".", 0, 1)
	if err != nil || len(list.Entries) != 1 || list.Entries[0].Path != "docs" {
		t.Fatalf("目录结果错误: %+v,%v", list, err)
	}
}

func TestTraversalSymlinkAndQuotaAreRejected(t *testing.T) {
	g := testGateway(t, nil)
	ctx := scopeContext("user", "chat")
	for _, name := range []string{"../secret", "a/../../secret", "/etc/passwd", "a\\b", ".", "a/../b"} {
		if _, err := g.Write(ctx, name, "内容", ""); err == nil {
			t.Fatalf("不应接受路径 %q", name)
		}
	}
	if _, err := g.Write(context.Background(), "a", "内容", ""); err == nil {
		t.Fatal("没有服务端身份不能创建工作区")
	}
	if _, err := g.Write(ctx, "large", strings.Repeat("x", int(g.config.MaxFileBytes)+1), ""); err == nil {
		t.Fatal("超大文件必须拒绝")
	}
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("不应可读"), 0600); err != nil {
		t.Fatal(err)
	}
	err := g.withWorkspace(ctx, func(ws *workspace) error { return os.Symlink(outside, filepath.Join(ws.path, "link")) })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Read(ctx, "link", 0, 0); err == nil {
		t.Fatal("工作区不能读取符号链接")
	}
}

func TestConcurrentWritesUseCompareAndSwap(t *testing.T) {
	g := testGateway(t, nil)
	ctx := scopeContext("user", "chat")
	created, _ := g.Write(ctx, "counter", "zero", "")
	var wait sync.WaitGroup
	var mu sync.Mutex
	success := 0
	for i := 0; i < 8; i++ {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			_, err := g.Write(ctx, "counter", fmt.Sprint(i), created.SHA256)
			if err == nil {
				mu.Lock()
				success++
				mu.Unlock()
			}
		}(i)
	}
	wait.Wait()
	if success != 1 {
		t.Fatalf("相同版本只能成功写一次，实际 %d 次", success)
	}
}

func TestShellCancellationAndInvalidSnapshotDoNotCommit(t *testing.T) {
	entered := make(chan struct{})
	g := testGateway(t, runnerFunc(func(ctx context.Context, s Snapshot, _ string) (ShellResult, Snapshot, error) {
		close(entered)
		<-ctx.Done()
		s.Files["new"] = File{Data: []byte("不应写入")}
		return ShellResult{}, s, ctx.Err()
	}))
	ctx, cancel := context.WithCancel(scopeContext("user", "chat"))
	if _, err := g.Write(ctx, "original", "原文", ""); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := g.Shell(ctx, "sleep 100"); done <- err }()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("Runner 未被调用: %v", err)
	case <-time.After(time.Second):
		cancel()
		t.Fatal("Runner 未在时限内启动")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("取消未传播: %v", err)
	}
	if _, err := g.Read(scopeContext("user", "chat"), "new", 0, 0); err == nil {
		t.Fatal("取消后不能提交快照")
	}
	g.runner = runnerFunc(func(_ context.Context, s Snapshot, _ string) (ShellResult, Snapshot, error) {
		s.Files["../escape"] = File{Data: []byte("越界")}
		return ShellResult{}, s, nil
	})
	if _, err := g.Shell(scopeContext("user", "chat"), "test"); err == nil {
		t.Fatal("Runner 返回值也必须重新检查路径")
	}
}

func TestArchiveRejectsLinksTraversalDuplicatesAndLimits(t *testing.T) {
	cfg := testConfig(t)
	for _, test := range []struct {
		name    string
		headers []*tar.Header
	}{
		{"符号链接", []*tar.Header{{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}}},
		{"硬链接", []*tar.Header{{Name: "link", Typeflag: tar.TypeLink, Linkname: "secret"}}},
		{"路径越界", []*tar.Header{{Name: "../secret", Typeflag: tar.TypeReg}}},
		{"绝对路径", []*tar.Header{{Name: "/secret", Typeflag: tar.TypeReg}}},
		{"设备", []*tar.Header{{Name: "device", Typeflag: tar.TypeChar}}},
		{"重复路径", []*tar.Header{{Name: "same", Typeflag: tar.TypeReg}, {Name: "same", Typeflag: tar.TypeReg}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var b bytes.Buffer
			w := tar.NewWriter(&b)
			for _, h := range test.headers {
				if err := w.WriteHeader(h); err != nil {
					t.Fatal(err)
				}
			}
			_ = w.Close()
			if _, err := unpackSnapshot(&b, cfg); err == nil {
				t.Fatal("不安全归档必须拒绝")
			}
		})
	}
	snapshot := Snapshot{Directories: []string{"empty"}, Files: map[string]File{"run.sh": {Data: []byte("echo ok"), Mode: 0700}}}
	archive, err := packSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := unpackSnapshot(bytes.NewReader(archive), cfg)
	if err != nil || string(restored.Files["run.sh"].Data) != "echo ok" || restored.Files["run.sh"].Mode&0100 == 0 || len(restored.Directories) != 1 {
		t.Fatalf("快照往返失败: %+v,%v", restored, err)
	}
}

func TestDockerOptionsAndOutputAreBounded(t *testing.T) {
	runner := &DockerRunner{config: testConfig(t)}
	args := strings.Join(runner.createArgs("test"), " ")
	for _, required := range []string{"--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges=true", "--user=65532:65532", "--pids-limit=64", "--memory=256m", "--pull=never", "--tmpfs=/workspace"} {
		if !strings.Contains(args, required) {
			t.Fatalf("缺少限制 %s", required)
		}
	}
	for _, forbidden := range []string{"--privileged", "--mount", "--volume", "docker.sock"} {
		if strings.Contains(args, forbidden) {
			t.Fatalf("禁止参数 %s", forbidden)
		}
	}
	output := &boundedOutput{limit: 8}
	if n, err := output.Write([]byte(strings.Repeat("x", 100))); err != nil || n != 100 || len(output.Bytes()) != 8 || !output.Truncated() {
		t.Fatal("输出必须有界且持续消费")
	}
}
