// Package tool 定义模型可请求、但只能由服务端授权执行的工具。
package tool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/example/agent-platform/backend/internal/execution"
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

// Tool 描述服务端执行契约。模型只能选择已注册工具；代码执行必须经过独立沙箱边界。
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

// Registry 是模型工具调用的白名单、授权和审计边界。
type Registry struct {
	mu               sync.RWMutex
	tools            map[string]*registeredTool
	versions         map[string]*registeredTool
	guard            ApprovalGuard
	policy           Policy
	maxArgumentBytes int
	maxResultBytes   int
	timeout          time.Duration
}

func NewRegistry(items ...Tool) *Registry {
	registry := &Registry{
		tools:            make(map[string]*registeredTool, len(items)),
		versions:         make(map[string]*registeredTool, len(items)),
		policy:           AllowAllPolicy{},
		maxArgumentBytes: DefaultMaxArgumentBytes,
		maxResultBytes:   DefaultMaxResultBytes,
		timeout:          DefaultExecutionTimeout,
	}
	for _, item := range items {
		if err := registry.Register(item); err != nil {
			panic(err)
		}
	}
	return registry
}

func NewDefaultRegistry(extra ...Tool) *Registry {
	return NewRegistry(append([]Tool{NewCalculator()}, extra...)...)
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
	r.mu.RLock()
	defer r.mu.RUnlock()
	definitions := make([]provider.ToolDefinition, 0, len(r.tools))
	for _, item := range r.tools {
		definition := item.definition
		definition.Function.Parameters = append([]byte(nil), definition.Function.Parameters...)
		definitions = append(definitions, definition)
	}
	sort.Slice(definitions, func(i, j int) bool { return definitions[i].Function.Name < definitions[j].Function.Name })
	return definitions
}

func (r *Registry) Execute(ctx context.Context, call provider.ToolCall) (string, error) {
	return r.ExecuteForUser(ctx, "", call)
}

// DefinitionsForUser 在发送给模型之前按权限过滤；执行时仍会再次授权。
func (r *Registry) DefinitionsForUser(ctx context.Context, userID string) []provider.ToolDefinition {
	return r.SnapshotForUser(ctx, userID).Definitions()
}

func (r *Registry) ExecuteForConversation(ctx context.Context, userID, conversationID string, call provider.ToolCall) (string, error) {
	return r.ExecuteForUser(execution.WithScope(ctx, execution.Scope{UserID: userID, ConversationID: conversationID}), userID, call)
}

// ExecuteForUser 是模型工具调用的唯一执行入口：
// 依次执行用户权限检查、参数大小限制、超时控制、结果大小校验，
// 并输出包含参数摘要的审计日志。
func (r *Registry) ExecuteForUser(ctx context.Context, userID string, call provider.ToolCall) (string, error) {
	return r.SnapshotForUser(ctx, userID).Execute(ctx, userID, call)
}

func (r *Registry) execute(ctx context.Context, userID string, item *registeredTool, call provider.ToolCall) (string, error) {
	if call.ID == "" {
		return "", fmt.Errorf("tool call id is required")
	}
	started := time.Now()
	// 参数审计：不直接记录参数原文，只记录字节数和内容摘要，
	// 既能定位问题又避免把用户数据无限制写进日志。
	argumentDigest := sha256.Sum256([]byte(call.Function.Arguments))
	auditFields := []logger.Field{
		logger.String("user_id", userID),
		logger.String("tool_call_id", call.ID),
		logger.String("tool_name", call.Function.Name),
		logger.String("tool_id", item.metadata.ID()),
		logger.String("tool_version", item.metadata.Version),
		logger.String("tool_source", item.metadata.Source),
		logger.String("tool_category", item.metadata.Category),
		logger.String("schema_hash", item.schemaHash),
		logger.Int("argument_bytes", len(call.Function.Arguments)),
		logger.String("argument_digest", hex.EncodeToString(argumentDigest[:8])),
	}
	if scope, err := execution.ScopeFromContext(ctx); err == nil {
		if scope.UserID != userID {
			return "", fmt.Errorf("工具执行身份不一致")
		}
		auditFields = append(auditFields, logger.String("conversation_id", scope.ConversationID), logger.String("tool_operation_id", scope.OperationID))
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
	if err := item.validate(call.Function.Arguments); err != nil {
		logger.Warn(ctx, "tool arguments rejected by schema", auditFields...)
		return "", err
	}
	timeout := r.timeout
	if timed, ok := item.Tool.(interface{ ExecutionTimeout() time.Duration }); ok && timed.ExecutionTimeout() > 0 {
		timeout = timed.ExecutionTimeout()
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if item.metadata.RequiresApproval {
		if r.guard == nil {
			logger.Warn(ctx, "tool approval service unavailable", auditFields...)
			return "", fmt.Errorf("工具需要用户审批，但审批服务未配置")
		}
		if err := r.guard.Authorize(execCtx, item.Descriptor(), call.Function.Arguments); err != nil {
			logger.Warn(ctx, "tool approval denied", auditFields...)
			return "", err
		}
	}
	result, execErr := item.Tool.Execute(execCtx, call.Function.Arguments)
	if execErr == nil && len(result) > r.maxResultBytes {
		result = ""
		execErr = fmt.Errorf("工具结果超过 %d 字节，请缩小范围或分页读取", r.maxResultBytes)
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
