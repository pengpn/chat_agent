package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/example/agent-platform/backend/internal/config"
)

type Message struct {
	Role             string     `json:"role"`
	Content          string     `json:"content"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
}

// ToolDefinition 是发送给模型的 OpenAI 兼容函数工具结构。
// 工具执行始终保留在服务进程中。
type ToolDefinition struct {
	Type     string                 `json:"type"`
	Function ToolFunctionDefinition `json:"function"`
}

type ToolFunctionDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// ToolCall 表示模型请求执行的一次函数调用。
type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolFunctionCall `json:"function"`
}

type ToolFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ChatRequest struct {
	Model           string
	Messages        []Message
	Tools           []ToolDefinition
	Thinking        *ThinkingConfig
	Temperature     *float64
	ResponseFormat  *ResponseFormat
	MaxOutputTokens int64
}

type ModelLimits struct {
	ContextWindow   int64
	MaxOutputTokens int64
}

// ResponseFormat 描述最终回答必须满足的 JSON Schema。Gateway 会统一注入
// 约束提示并校验结果，支持原生 Structured Outputs 的 Provider 还会映射为
// 对应的供应商协议字段。
type ResponseFormat struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Schema      json.RawMessage `json:"schema"`
	Strict      bool            `json:"strict"`
}

// ThinkingConfig 在所选 Provider 和模型支持时请求模型返回推理内容。
// BudgetTokens 当前用于 Anthropic 的原生 API。
type ThinkingConfig struct {
	Enabled      bool
	BudgetTokens int
}

// ChatResponse 表示完整的非流式模型结果。
type ChatResponse struct {
	Content          string
	Think            string
	ToolCalls        []ToolCall
	Usage            *TokenUsage
	StructuredOutput json.RawMessage
}

type StreamEvent struct {
	// Sequence 由 LLMGateway 在解析供应商协议后分配，与对外的 SSE 序号不同；
	// 后者还包含元数据和最终缓存事件。
	Sequence         int64
	Delta            string          // 最终回答文本增量
	ThinkDelta       string          // 思考内容增量
	ToolCalls        []ToolCall      // 模型请求的工具调用
	Usage            *TokenUsage     // token 用量
	Connected        bool            // 已收到供应商的成功 HTTP 响应头
	Activity         bool            // 已收到供应商的首个协议事件
	Done             bool            // 当前模型请求正常结束
	FinishReason     string          // 供应商报告的结束原因
	StructuredOutput json.RawMessage // 通过 Schema 校验的完整 JSON，仅在 Done 事件中返回
	Err              error           // 当前请求失败
}

// TokenUsage 包含模型 API 上报的 Token 用量。流式 API 通常发送累积用量快照，
// 因此合并时取各项最大值。
type TokenUsage struct {
	InputTokens  int64
	OutputTokens int64
	TotalTokens  int64
}

func (u *TokenUsage) Merge(other TokenUsage) {
	u.InputTokens = max(u.InputTokens, other.InputTokens)
	u.OutputTokens = max(u.OutputTokens, other.OutputTokens)
	u.TotalTokens = max(u.TotalTokens, other.TotalTokens)
	if u.TotalTokens < u.InputTokens+u.OutputTokens {
		u.TotalTokens = u.InputTokens + u.OutputTokens
	}
}

// Add 汇总多次独立模型请求的用量，例如工具调用前后的请求。
func (u *TokenUsage) Add(other TokenUsage) {
	u.InputTokens += other.InputTokens
	u.OutputTokens += other.OutputTokens
	if other.TotalTokens > 0 {
		u.TotalTokens += other.TotalTokens
	} else {
		u.TotalTokens += other.InputTokens + other.OutputTokens
	}
}

type Provider interface {
	Chat(ctx context.Context, request ChatRequest) (ChatResponse, error)
	ChatStream(ctx context.Context, request ChatRequest) <-chan StreamEvent
}

// Resolver 为聊天服务提供已配置的 Provider 及其协议能力。
// 将其定义为接口后，服务测试无需真实模型端点即可驱动工具调用循环。
type Resolver interface {
	Get(id, model string) (Provider, error)
	SupportsTools(id string) bool
	SupportsThinking(id, model string) bool
	CalculateCost(providerID, model string, usage TokenUsage) (int64, bool)
	Limits(providerID, model string) (ModelLimits, bool)
	Tokenizer(providerID, model string) string
}

func (r *Registry) Limits(providerID, model string) (ModelLimits, bool) {
	cfg, ok := r.configs[providerID]
	if !ok {
		return ModelLimits{}, false
	}
	limits, ok := cfg.ModelLimits[model]
	if !ok {
		return ModelLimits{}, false
	}
	return ModelLimits{ContextWindow: limits.ContextWindow, MaxOutputTokens: limits.MaxOutputTokens}, true
}

func (r *Registry) Tokenizer(providerID, model string) string {
	cfg, ok := r.configs[providerID]
	if !ok {
		return "heuristic"
	}
	if tokenizerName := cfg.ModelTokenizers[model]; tokenizerName != "" {
		return tokenizerName
	}
	return "heuristic"
}

type PublicProvider struct {
	ID     string   `json:"id"`
	Label  string   `json:"label"`
	Models []string `json:"models"`
}

type Registry struct {
	providers map[string]Provider
	configs   map[string]config.ProviderConfig
}

func NewRegistry(configs []config.ProviderConfig) *Registry {
	registry := &Registry{
		providers: make(map[string]Provider, len(configs)),
		configs:   make(map[string]config.ProviderConfig, len(configs)),
	}
	for _, cfg := range configs {
		var instance Provider
		switch cfg.Type {
		case "openai-compatible":
			instance = NewOpenAICompatible(cfg)
		case "anthropic":
			instance = NewAnthropic(cfg)
		default:
			continue
		}
		registry.providers[cfg.ID] = instance
		registry.configs[cfg.ID] = cfg
	}
	return registry
}

func (r *Registry) Get(id, model string) (Provider, error) {
	provider, ok := r.providers[id]
	if !ok {
		return nil, fmt.Errorf("provider %q is not configured", id)
	}
	config := r.configs[id]
	for _, allowedModel := range config.Models {
		if allowedModel == model {
			return provider, nil
		}
	}
	return nil, fmt.Errorf("model %q is not enabled for provider %q", model, id)
}

// SupportsTools 返回已配置 Provider 是否接受 OpenAI 函数调用的请求和响应结构。
func (r *Registry) SupportsTools(id string) bool {
	cfg, ok := r.configs[id]
	return ok && cfg.Type == "openai-compatible"
}

// SupportsThinking 返回是否可要求该 Provider 使用指定模型返回推理内容。
// 通用 OpenAI 和 Kimi 请求被刻意排除，因为它们没有共享且安全的标准思考参数。
func (r *Registry) SupportsThinking(id, model string) bool {
	cfg, ok := r.configs[id]
	if !ok {
		return false
	}
	switch cfg.ID {
	case "deepseek":
		return strings.HasPrefix(model, "deepseek-v4-")
	case "glm":
		return strings.HasPrefix(model, "glm-5") ||
			strings.HasPrefix(model, "glm-4.7") ||
			strings.HasPrefix(model, "glm-4.6") ||
			strings.HasPrefix(model, "glm-4.5")
	case "anthropic":
		return strings.Contains(model, "sonnet") || strings.Contains(model, "opus")
	default:
		return false
	}
}

func (r *Registry) Public() []PublicProvider {
	result := make([]PublicProvider, 0, len(r.configs))
	for _, cfg := range r.configs {
		result = append(result, PublicProvider{ID: cfg.ID, Label: cfg.Label, Models: cfg.Models})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Label < result[j].Label })
	return result
}

func (r *Registry) IDs() []string {
	result := make([]string, 0, len(r.providers))
	for id := range r.providers {
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}

// CalculateCost 使用部署配置的价格，将 Provider 上报的 Token 用量换算为纳美元。
// 所选模型未配置价格时返回 false。
func (r *Registry) CalculateCost(providerID, model string, usage TokenUsage) (int64, bool) {
	cfg, ok := r.configs[providerID]
	if !ok {
		return 0, false
	}
	pricing, ok := cfg.ModelPricing[model]
	if !ok {
		return 0, false
	}
	// 美元/百万 Token × Token 数 × 10^9 纳美元/10^6 Token。
	cost := float64(usage.InputTokens)*pricing.InputUSDPerMillionTokens*1_000 +
		float64(usage.OutputTokens)*pricing.OutputUSDPerMillionTokens*1_000
	return int64(math.Round(cost)), true
}
