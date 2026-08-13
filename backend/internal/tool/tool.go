// Package tool contains the server-owned tools that a model may request.
package tool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"time"

	"github.com/example/agent-platform/backend/internal/logger"
	"github.com/example/agent-platform/backend/internal/provider"
)

// 工具安全默认值：参数与结果大小限制防止模型构造超大输入或把超大结果
// 塞回上下文；执行超时防止单个工具拖住整条生成链路。
const (
	DefaultMaxArgumentBytes = 64 << 10
	DefaultMaxResultBytes   = 32 << 10
	DefaultExecutionTimeout = 30 * time.Second
)

// Tool describes a strictly server-executed function. Models can choose a
// registered tool, but never provide executable code or network destinations.
type Tool interface {
	Definition() provider.ToolDefinition
	Execute(ctx context.Context, arguments string) (string, error)
}

// Policy 判断某个用户是否有权调用某个工具。
// 实现必须是无副作用的；拒绝原因由 Registry 统一记录审计日志。
type Policy interface {
	Allowed(ctx context.Context, userID, toolName string) (bool, error)
}

// AllowAllPolicy 是默认策略：所有登录用户都可以调用全部已注册工具。
type AllowAllPolicy struct{}

func (AllowAllPolicy) Allowed(context.Context, string, string) (bool, error) { return true, nil }

// RoleLookup 供 RolePolicy 查询用户角色，避免工具包直接依赖仓储层。
type RoleLookup interface {
	IsAdmin(ctx context.Context, userID string) (bool, error)
}

// RolePolicy 把 adminOnly 集合中的工具限制为管理员专属。
// 这是“用户权限”的最小可用形态：更细粒度的授权可以替换 Policy 实现。
type RolePolicy struct {
	lookup    RoleLookup
	adminOnly map[string]bool
}

func NewRolePolicy(lookup RoleLookup, adminOnlyTools []string) *RolePolicy {
	adminOnly := make(map[string]bool, len(adminOnlyTools))
	for _, name := range adminOnlyTools {
		if name != "" {
			adminOnly[name] = true
		}
	}
	return &RolePolicy{lookup: lookup, adminOnly: adminOnly}
}

func (p *RolePolicy) Allowed(ctx context.Context, userID, toolName string) (bool, error) {
	if !p.adminOnly[toolName] {
		return true, nil
	}
	if p.lookup == nil {
		return false, nil
	}
	return p.lookup.IsAdmin(ctx, userID)
}

// SafetyConfig 收敛工具执行边界的全部可调参数。
type SafetyConfig struct {
	Policy           Policy
	MaxArgumentBytes int
	MaxResultBytes   int
	Timeout          time.Duration
}

// Registry is the allow-list and execution boundary for model tool calls.
type Registry struct {
	tools            map[string]Tool
	policy           Policy
	maxArgumentBytes int
	maxResultBytes   int
	timeout          time.Duration
}

func NewRegistry(items ...Tool) *Registry {
	registry := &Registry{
		tools:            make(map[string]Tool, len(items)),
		policy:           AllowAllPolicy{},
		maxArgumentBytes: DefaultMaxArgumentBytes,
		maxResultBytes:   DefaultMaxResultBytes,
		timeout:          DefaultExecutionTimeout,
	}
	for _, item := range items {
		definition := item.Definition()
		registry.tools[definition.Function.Name] = item
	}
	return registry
}

func NewDefaultRegistry() *Registry {
	return NewRegistry(NewCalculator())
}

// Configure 覆盖默认安全参数。零值字段沿用默认值。
func (r *Registry) Configure(config SafetyConfig) {
	if config.Policy != nil {
		r.policy = config.Policy
	}
	if config.MaxArgumentBytes > 0 {
		r.maxArgumentBytes = config.MaxArgumentBytes
	}
	if config.MaxResultBytes > 0 {
		r.maxResultBytes = config.MaxResultBytes
	}
	if config.Timeout > 0 {
		r.timeout = config.Timeout
	}
}

func (r *Registry) Definitions() []provider.ToolDefinition {
	definitions := make([]provider.ToolDefinition, 0, len(r.tools))
	for _, item := range r.tools {
		definitions = append(definitions, item.Definition())
	}
	sort.Slice(definitions, func(i, j int) bool { return definitions[i].Function.Name < definitions[j].Function.Name })
	return definitions
}

func (r *Registry) Execute(ctx context.Context, call provider.ToolCall) (string, error) {
	return r.ExecuteForUser(ctx, "", call)
}

// ExecuteForUser 是模型工具调用的唯一执行入口：
// 依次执行用户权限检查、参数大小限制、超时控制、结果截断，
// 并输出包含参数摘要的审计日志。
func (r *Registry) ExecuteForUser(ctx context.Context, userID string, call provider.ToolCall) (string, error) {
	if call.ID == "" {
		return "", fmt.Errorf("tool call id is required")
	}
	item, ok := r.tools[call.Function.Name]
	if !ok {
		return "", fmt.Errorf("tool %q is not registered", call.Function.Name)
	}
	started := time.Now()
	// 参数审计：不直接记录参数原文，只记录字节数和内容摘要，
	// 既能定位问题又避免把用户数据无限制写进日志。
	argumentDigest := sha256.Sum256([]byte(call.Function.Arguments))
	auditFields := []logger.Field{
		logger.String("user_id", userID),
		logger.String("tool_call_id", call.ID),
		logger.String("tool_name", call.Function.Name),
		logger.Int("argument_bytes", len(call.Function.Arguments)),
		logger.String("argument_digest", hex.EncodeToString(argumentDigest[:8])),
	}
	allowed, err := r.policy.Allowed(ctx, userID, call.Function.Name)
	if err != nil {
		logger.Warn(ctx, "tool permission check failed", append(auditFields, logger.Err(err))...)
		return "", fmt.Errorf("tool permission check failed: %w", err)
	}
	if !allowed {
		logger.Warn(ctx, "tool call denied by policy", auditFields...)
		return "", fmt.Errorf("tool %q is not allowed for this user", call.Function.Name)
	}
	if len(call.Function.Arguments) > r.maxArgumentBytes {
		logger.Warn(ctx, "tool call arguments too large", auditFields...)
		return "", fmt.Errorf("tool arguments exceed %d bytes", r.maxArgumentBytes)
	}
	execCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	result, execErr := item.Execute(execCtx, call.Function.Arguments)
	if execErr == nil && len(result) > r.maxResultBytes {
		result = result[:r.maxResultBytes]
	}
	status := "completed"
	if execErr != nil {
		status = "failed"
	}
	logger.Info(ctx, "tool execution audit", append(auditFields,
		logger.String("status", status),
		logger.Int64("duration_ms", time.Since(started).Milliseconds()),
		logger.Int("result_bytes", len(result)),
	)...)
	return result, execErr
}
