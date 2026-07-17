// Package tool contains the server-owned tools that a model may request.
package tool

import (
	"context"
	"fmt"
	"sort"

	"github.com/example/agent-platform/backend/internal/provider"
)

// Tool describes a strictly server-executed function. Models can choose a
// registered tool, but never provide executable code or network destinations.
type Tool interface {
	Definition() provider.ToolDefinition
	Execute(ctx context.Context, arguments string) (string, error)
}

// Registry is the allow-list and execution boundary for model tool calls.
type Registry struct {
	tools map[string]Tool
}

func NewRegistry(items ...Tool) *Registry {
	registry := &Registry{tools: make(map[string]Tool, len(items))}
	for _, item := range items {
		definition := item.Definition()
		registry.tools[definition.Function.Name] = item
	}
	return registry
}

func NewDefaultRegistry() *Registry {
	return NewRegistry(NewCalculator())
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
	if call.ID == "" {
		return "", fmt.Errorf("tool call id is required")
	}
	item, ok := r.tools[call.Function.Name]
	if !ok {
		return "", fmt.Errorf("tool %q is not registered", call.Function.Name)
	}
	return item.Execute(ctx, call.Function.Arguments)
}
