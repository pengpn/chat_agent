package provider

import (
	"context"
	"fmt"
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
	Done  bool
	Err   error
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
