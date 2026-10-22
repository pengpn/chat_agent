package config

import (
	"fmt"
	"strings"
	"time"
)

// ExecutionConfig 只由部署者配置，模型不能指定宿主目录、镜像、网络或资源权限。
type ExecutionConfig struct {
	Enabled           bool
	AdminOnly         bool
	WorkspaceRoot     string
	DockerBinary      string
	Image             string
	Timeout           time.Duration
	MemoryMB          int
	CPUs              int
	PIDs              int
	MaxConcurrent     int
	MaxWorkspaceBytes int64
	MaxFileBytes      int64
	MaxEntries        int
	MaxOutputBytes    int
}

func loadExecutionConfig() (ExecutionConfig, error) {
	cfg := ExecutionConfig{
		WorkspaceRoot: strings.TrimSpace(env("EXECUTION_WORKSPACE_ROOT", "./data/tool-workspaces")),
		DockerBinary:  strings.TrimSpace(env("EXECUTION_DOCKER_BINARY", "docker")),
		Image:         strings.TrimSpace(env("EXECUTION_IMAGE", "chat-agent-sandbox:local")),
	}
	var err error
	if cfg.Enabled, err = boolEnv("EXECUTION_ENABLED", false); err != nil {
		return cfg, err
	}
	if cfg.AdminOnly, err = boolEnv("EXECUTION_ADMIN_ONLY", true); err != nil {
		return cfg, err
	}
	if cfg.Timeout, err = positiveDurationEnv("EXECUTION_TIMEOUT", "20s"); err != nil {
		return cfg, err
	}
	if cfg.MemoryMB, err = positiveIntEnv("EXECUTION_MEMORY_MB", 256); err != nil {
		return cfg, err
	}
	if cfg.CPUs, err = positiveIntEnv("EXECUTION_CPUS", 1); err != nil {
		return cfg, err
	}
	if cfg.PIDs, err = positiveIntEnv("EXECUTION_PIDS", 64); err != nil {
		return cfg, err
	}
	if cfg.MaxConcurrent, err = positiveIntEnv("EXECUTION_MAX_CONCURRENT", 2); err != nil {
		return cfg, err
	}
	if cfg.MaxWorkspaceBytes, err = positiveInt64Env("EXECUTION_MAX_WORKSPACE_BYTES", 16<<20); err != nil {
		return cfg, err
	}
	if cfg.MaxFileBytes, err = positiveInt64Env("EXECUTION_MAX_FILE_BYTES", 1<<20); err != nil {
		return cfg, err
	}
	if cfg.MaxEntries, err = positiveIntEnv("EXECUTION_MAX_ENTRIES", 512); err != nil {
		return cfg, err
	}
	if cfg.MaxOutputBytes, err = positiveIntEnv("EXECUTION_MAX_OUTPUT_BYTES", 4096); err != nil {
		return cfg, err
	}
	if cfg.WorkspaceRoot == "" || cfg.DockerBinary == "" || cfg.Image == "" || strings.HasPrefix(cfg.Image, "-") || strings.ContainsAny(cfg.Image, " \t\r\n") {
		return cfg, fmt.Errorf("执行工具的目录、Docker 程序和镜像配置无效")
	}
	if cfg.Timeout > time.Minute || cfg.MemoryMB < 64 || cfg.MemoryMB > 2048 || cfg.CPUs > 4 || cfg.PIDs > 256 || cfg.MaxConcurrent > 16 || cfg.MaxEntries > 4096 {
		return cfg, fmt.Errorf("执行工具资源配置超过学习版安全上限")
	}
	if cfg.MaxWorkspaceBytes > 128<<20 || cfg.MaxFileBytes > cfg.MaxWorkspaceBytes || 2*cfg.MaxWorkspaceBytes+(16<<20) >= int64(cfg.MemoryMB)<<20 {
		return cfg, fmt.Errorf("工作区容量必须容纳单文件，并为两倍容量内存盘和临时目录预留容器内存")
	}
	return cfg, nil
}
