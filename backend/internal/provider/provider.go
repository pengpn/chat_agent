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

// ToolDefinition is the OpenAI-compatible function tool schema sent to a
// model. Tool execution itself always remains in the service process.
type ToolDefinition struct {
	Type     string                 `json:"type"`
	Function ToolFunctionDefinition `json:"function"`
}

type ToolFunctionDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// ToolCall is a function invocation requested by a model.
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
	Model       string
	Messages    []Message
	Tools       []ToolDefinition
	Thinking    *ThinkingConfig
	Temperature *float64
}

// ThinkingConfig requests model reasoning when the selected Provider and model
// support it. BudgetTokens is currently used by Anthropic's native API.
type ThinkingConfig struct {
	Enabled      bool
	BudgetTokens int
}

// ChatResponse is the complete, non-streaming model result.
type ChatResponse struct {
	Content   string
	Think     string
	ToolCalls []ToolCall
	Usage     *TokenUsage
}

type StreamEvent struct {
	Delta        string      // 最终回答文本增量
	ThinkDelta   string      // 思考内容增量
	ToolCalls    []ToolCall  // 模型请求的工具调用
	Usage        *TokenUsage // token 用量
	Connected    bool        // 已收到供应商的成功 HTTP 响应头
	Activity     bool        // 已收到供应商的首个协议事件
	Done         bool        // 当前模型请求正常结束
	FinishReason string      // 供应商报告的结束原因
	Err          error       // 当前请求失败
}

// TokenUsage contains token counts reported by a model API. Counts are merged
// by maximum because streaming APIs typically emit cumulative usage snapshots.
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

// Add aggregates usage across separate model requests, such as the requests
// made before and after a tool invocation.
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

// Resolver supplies the configured Provider and its protocol capabilities to
// the chat service. Keeping this as an interface lets service tests drive the
// tool loop without a real model endpoint.
type Resolver interface {
	Get(id, model string) (Provider, error)
	SupportsTools(id string) bool
	SupportsThinking(id, model string) bool
	CalculateCost(providerID, model string, usage TokenUsage) (int64, bool)
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

// SupportsTools reports whether the configured provider accepts the OpenAI
// function-calling request and response schema.
func (r *Registry) SupportsTools(id string) bool {
	cfg, ok := r.configs[id]
	return ok && cfg.Type == "openai-compatible"
}

// SupportsThinking reports whether the Provider can be asked to return
// reasoning with this model. It deliberately excludes generic OpenAI and Kimi
// requests: they do not share a safe, standard thinking request parameter.
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

// CalculateCost converts provider-reported token usage into nano USD using
// deployment-provided pricing. It returns false when no price is configured
// for the selected model.
func (r *Registry) CalculateCost(providerID, model string, usage TokenUsage) (int64, bool) {
	cfg, ok := r.configs[providerID]
	if !ok {
		return 0, false
	}
	pricing, ok := cfg.ModelPricing[model]
	if !ok {
		return 0, false
	}
	// USD / million tokens * tokens * 1e9 nano USD / 1e6 tokens.
	cost := float64(usage.InputTokens)*pricing.InputUSDPerMillionTokens*1_000 +
		float64(usage.OutputTokens)*pricing.OutputUSDPerMillionTokens*1_000
	return int64(math.Round(cost)), true
}
