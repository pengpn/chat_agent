package execution

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/example/agent-platform/backend/internal/config"
	"github.com/example/agent-platform/backend/internal/logger"
	"github.com/google/uuid"
)

// DockerRunner 只能调用部署者指定的 Docker CLI，绝不会把模型命令交给宿主 sh/bash。
type DockerRunner struct {
	config config.ExecutionConfig
	binary string
}

// controlError 保留诊断信息供测试与运维检查，但 Error 不向模型暴露宿主路径。
type controlError struct{ step, detail string }

func (e *controlError) Error() string {
	return fmt.Sprintf("Docker %s 失败，请检查运行时、镜像和权限配置", e.step)
}

func NewDockerRunner(cfg config.ExecutionConfig) (*DockerRunner, error) {
	binary, err := exec.LookPath(cfg.DockerBinary)
	if err != nil {
		return nil, fmt.Errorf("执行工具需要已安装的 Docker CLI: %w", err)
	}
	return &DockerRunner{config: cfg, binary: binary}, nil
}

// createArgs 固定禁止网络、宿主挂载和特权；两个可写目录都是有容量上限的 tmpfs。
func (r *DockerRunner) createArgs(name string) []string {
	cfg := r.config
	lifetime := strconv.Itoa(int(math.Ceil(cfg.Timeout.Seconds())) + 15)
	return []string{"create", "--pull=never", "--name", name, "--label", "chat_agent.execution=true",
		"--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges=true",
		"--user=65532:65532", "--pids-limit=" + strconv.Itoa(cfg.PIDs), "--memory=" + strconv.Itoa(cfg.MemoryMB) + "m",
		"--memory-swap=" + strconv.Itoa(cfg.MemoryMB) + "m", "--cpus=" + strconv.Itoa(cfg.CPUs), "--restart=no",
		"--workdir=/workspace", "--env=HOME=/tmp", "--env=TMPDIR=/tmp",
		"--tmpfs=/workspace:rw,nosuid,nodev,size=" + strconv.FormatInt(2*cfg.MaxWorkspaceBytes, 10) + ",mode=0700,uid=65532,gid=65532",
		"--tmpfs=/tmp:rw,nosuid,nodev,noexec,size=16777216,mode=1777",
		"--entrypoint=/bin/sh", cfg.Image, "-c", "exec sleep " + lifetime}
}

func (r *DockerRunner) Run(ctx context.Context, snapshot Snapshot, command string) (result ShellResult, updated Snapshot, runErr error) {
	name := "chat-agent-exec-" + uuid.NewString()
	// 使用独立清理 Context；不能只杀掉 Docker 客户端而遗留容器内的模型命令。
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := r.control(cleanupCtx, nil, "rm", "--force", name); err != nil {
			logger.Warn(ctx, "sandbox cleanup failed", logger.String("container", name), logger.Err(err))
		}
	}()
	if err := r.control(ctx, nil, r.createArgs(name)...); err != nil {
		return result, updated, err
	}
	if err := r.control(ctx, nil, "start", name); err != nil {
		return result, updated, err
	}
	archive, err := packSnapshot(snapshot)
	if err != nil {
		return result, updated, err
	}
	// docker cp 的归档接口拒绝只读根文件系统，即使目的地是 tmpfs；
	// 通过容器内固定的 tar 命令传输，不放宽根文件系统权限。
	if err := r.control(ctx, bytes.NewReader(archive), "exec", "-i", "--user=65532:65532", name, "/bin/tar", "-xf", "-", "-C", "/workspace"); err != nil {
		return result, updated, err
	}
	output := &boundedOutput{limit: r.config.MaxOutputBytes}
	cmd := exec.CommandContext(ctx, r.binary, "exec", "--user=65532:65532", "--workdir=/workspace", name, "/bin/sh", "-lc", command)
	cmd.Stdout, cmd.Stderr = output, output
	err = cmd.Run()
	result.Output, result.Truncated = strings.ToValidUTF8(string(output.Bytes()), "�"), output.Truncated()
	if ctx.Err() != nil {
		return result, updated, ctx.Err()
	}
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return result, updated, fmt.Errorf("无法启动容器内命令")
		}
		result.ExitCode = exit.ExitCode()
	}
	// SIGSTOP 的 -1 只作用于当前 PID 命名空间内有权限的进程，排除发信号者和 PID 1。
	// 模型及其子进程均为同一非 root UID，不能留下后台写入者；新的导出进程仍可运行。
	if err := r.control(ctx, nil, "exec", "--user=65532:65532", name, "/bin/kill", "-STOP", "-1"); err != nil {
		var failure *controlError
		if !errors.As(err, &failure) || !strings.Contains(failure.detail, "No such process") {
			return result, updated, err
		}
	}
	maximumArchive := r.config.MaxWorkspaceBytes + int64(r.config.MaxEntries)*4096 + (1 << 20)
	tarOutput := &boundedOutput{limit: int(maximumArchive)}
	copyCmd := exec.CommandContext(ctx, r.binary, "exec", "--user=65532:65532", name, "/bin/tar", "-cf", "-", "-C", "/workspace", ".")
	copyCmd.Stdout = tarOutput
	copyCmd.Stderr = &boundedOutput{limit: 2048}
	if err := copyCmd.Run(); err != nil {
		if ctx.Err() != nil {
			return result, updated, ctx.Err()
		}
		return result, updated, fmt.Errorf("导出容器工作区失败")
	}
	if tarOutput.Truncated() {
		return result, updated, fmt.Errorf("容器工作区归档超出上限")
	}
	updated, err = unpackSnapshot(bytes.NewReader(tarOutput.Bytes()), r.config)
	if err != nil {
		return result, Snapshot{}, err
	}
	return result, updated, nil
}

func (r *DockerRunner) control(ctx context.Context, input io.Reader, args ...string) error {
	cmd := exec.CommandContext(ctx, r.binary, args...)
	cmd.Stdin = input
	output := &boundedOutput{limit: 2048}
	cmd.Stdout, cmd.Stderr = output, output
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// 不把 Docker CLI 原始输出（可能包含宿主路径）交给模型。
		return &controlError{step: args[0], detail: string(output.Bytes())}
	}
	return nil
}

// boundedOutput 达到上限后继续消费并丢弃，既限制内存，也避免管道反压阻塞命令退出。
type boundedOutput struct {
	mu        sync.Mutex
	data      []byte
	limit     int
	truncated bool
}

func (b *boundedOutput) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	keep := min(max(0, b.limit-len(b.data)), len(data))
	b.data = append(b.data, data[:keep]...)
	if keep < len(data) {
		b.truncated = true
	}
	return len(data), nil
}
func (b *boundedOutput) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.data...)
}
func (b *boundedOutput) Truncated() bool { b.mu.Lock(); defer b.mu.Unlock(); return b.truncated }
