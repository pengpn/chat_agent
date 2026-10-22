package execution

import (
	"context"
	"errors"
	"flag"
	"os/exec"
	"strings"
	"testing"
	"time"
)

var dockerSandboxTest = flag.Bool("docker-sandbox", false, "显式开启真实 Docker 沙箱测试")
var dockerSandboxImage = flag.String("sandbox-image", "chat-agent-sandbox:local", "真实沙箱测试使用的已存在镜像，不自动拉取")

func TestDockerSandboxIsolationSyncAndCancellation(t *testing.T) {
	if !*dockerSandboxTest {
		t.Skip("使用 -args -docker-sandbox 显式开启，不默认操作 Docker")
	}
	cfg := testConfig(t)
	cfg.Image = *dockerSandboxImage
	runner, err := NewDockerRunner(cfg)
	if err != nil {
		t.Fatal(err)
	}
	g, err := New(cfg, runner)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	ctx := scopeContext("integration-user", "integration-chat")
	// 从空工作区启动也应支持，归档不能要求先手动创建文件。
	// 部分 Docker Desktop 内核会显示未配置的隧道接口；检查路由而不是假设接口数量。
	result, err := g.Shell(ctx, `test "$(id -u)" = 65532 && test "$(wc -l < /proc/net/route)" -eq 1 && test ! -S /var/run/docker.sock && test ! -f /workspace/backend/.env && ! touch /host-write 2>/dev/null && printf 'hello sandbox' > sample.txt && printf 'ok'`)
	if err != nil || result.ExitCode != 0 || result.Output != "ok" || !result.WorkspaceSynced {
		var diagnostic *controlError
		if errors.As(err, &diagnostic) {
			t.Logf("Docker 诊断: %s", diagnostic.detail)
		}
		t.Fatalf("真实沙箱执行失败: %+v, %v", result, err)
	}
	file, err := g.Read(ctx, "sample.txt", 0, 0)
	if err != nil || file.Content != "hello sandbox" {
		t.Fatalf("文件未同步: %+v, %v", file, err)
	}
	// 非零退出码是命令结果，不应该伪装成工具基础设施故障。
	result, err = g.Shell(ctx, `printf failed; exit 7`)
	if err != nil || result.ExitCode != 7 || result.Output != "failed" {
		t.Fatalf("退出码丢失: %+v, %v", result, err)
	}
	result, err = g.Shell(ctx, `awk 'BEGIN { for (i=0;i<10000;i++) printf "x" }'`)
	if err != nil || !result.Truncated || len(result.Output) != cfg.MaxOutputBytes {
		t.Fatalf("真实命令输出未受限制: %+v,%v", result, err)
	}
	result, err = g.Shell(ctx, `(while :; do printf background > background.txt; done) >/dev/null 2>&1 & printf done`)
	if err != nil || result.Output != "done" {
		t.Fatalf("后台进程未在导出前冻结: %+v,%v", result, err)
	}
	// 不能把容器内创建的链接导入宿主快照，整次变更都应丢弃。
	if _, err := g.Shell(ctx, `printf changed > sample.txt; ln -s /etc/passwd link`); err == nil {
		t.Fatal("含符号链接的导出应拒绝")
	}
	file, _ = g.Read(ctx, "sample.txt", 0, 0)
	if file.Content != "hello sandbox" {
		t.Fatal("无效快照不能部分提交")
	}
	cancelCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, err = g.Shell(cancelCtx, `printf canceled > sample.txt; sleep 60`)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("命令超时没有向上传播: %v", err)
	}
	file, _ = g.Read(ctx, "sample.txt", 0, 0)
	if file.Content != "hello sandbox" {
		t.Fatal("取消不能同步容器内的半成品")
	}
	checkCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	output, err := exec.CommandContext(checkCtx, cfg.DockerBinary, "ps", "-aq", "--filter", "label=chat_agent.execution=true").Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(output)) != "" {
		t.Fatalf("存在未清理的测试沙箱容器: %s", output)
	}
}
