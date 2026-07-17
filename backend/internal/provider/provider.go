package provider

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/example/agent-platform/backend/internal/config"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatRequest struct {
	Model       string
	Messages    []Message
	Temperature *float64
}

type StreamEvent struct {
	Delta string
	Usage *TokenUsage
	Done  bool
	Err   error
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

type Provider interface {
	ChatStream(ctx context.Context, request ChatRequest) <-chan StreamEvent
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
