package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/example/agent-platform/backend/internal/provider"
)

type scriptedTool struct {
	name      string
	result    string
	err       error
	delay     time.Duration
	lastArgs  string
	ctxClosed bool
}

func (s *scriptedTool) Definition() provider.ToolDefinition {
	return provider.ToolDefinition{Type: "function", Function: provider.ToolFunctionDefinition{Name: s.name, Description: "test tool", Parameters: json.RawMessage(`{"type":"object"}`)}}
}

func (s *scriptedTool) Execute(ctx context.Context, arguments string) (string, error) {
	s.lastArgs = arguments
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			s.ctxClosed = true
			return "", ctx.Err()
		}
	}
	return s.result, s.err
}

func callFor(name, arguments string) provider.ToolCall {
	return provider.ToolCall{ID: "call-1", Type: "function", Function: provider.ToolFunctionCall{Name: name, Arguments: arguments}}
}

func TestRegistryRejectsOversizedArguments(t *testing.T) {
	item := &scriptedTool{name: "demo"}
	registry := NewRegistry(item)
	registry.Configure(SafetyConfig{MaxArgumentBytes: 8})

	_, err := registry.ExecuteForUser(context.Background(), "user-1", callFor("demo", strings.Repeat("a", 16)))
	if err == nil || item.lastArgs != "" {
		t.Fatalf("err = %v, tool must not execute oversized arguments", err)
	}
}

func TestRegistryRejectsOversizedResultWithoutBreakingJSON(t *testing.T) {
	item := &scriptedTool{name: "demo", result: strings.Repeat("b", 64)}
	registry := NewRegistry(item)
	registry.Configure(SafetyConfig{MaxResultBytes: 16})

	result, err := registry.ExecuteForUser(context.Background(), "user-1", callFor("demo", "{}"))
	if err == nil || result != "" {
		t.Fatalf("result length = %d, err = %v", len(result), err)
	}
}

func TestRegistryEnforcesExecutionTimeout(t *testing.T) {
	item := &scriptedTool{name: "demo", delay: time.Second, result: "late"}
	registry := NewRegistry(item)
	registry.Configure(SafetyConfig{Timeout: 20 * time.Millisecond})

	_, err := registry.ExecuteForUser(context.Background(), "user-1", callFor("demo", "{}"))
	if !errors.Is(err, context.DeadlineExceeded) || !item.ctxClosed {
		t.Fatalf("err = %v, ctxClosed = %v", err, item.ctxClosed)
	}
}

type denyPolicy struct{ deniedTool string }

func (p denyPolicy) Allowed(_ context.Context, _, toolName string) (bool, error) {
	return toolName != p.deniedTool, nil
}

func TestRegistryDeniesByPolicy(t *testing.T) {
	item := &scriptedTool{name: "admin_tool"}
	registry := NewRegistry(item)
	registry.Configure(SafetyConfig{Policy: denyPolicy{deniedTool: "admin_tool"}})

	_, err := registry.ExecuteForUser(context.Background(), "user-1", callFor("admin_tool", "{}"))
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("err = %v", err)
	}
	if item.lastArgs != "" {
		t.Fatal("denied tool must not execute")
	}
}

type stubRoleLookup struct{ admin bool }

func (s stubRoleLookup) IsAdmin(context.Context, string) (bool, error) { return s.admin, nil }

func TestRolePolicyRestrictsAdminOnlyTools(t *testing.T) {
	policy := NewRolePolicy(stubRoleLookup{admin: false}, []string{"admin_tool"})
	allowed, err := policy.Allowed(context.Background(), "user-1", "admin_tool")
	if err != nil || allowed {
		t.Fatalf("allowed = %v, err = %v", allowed, err)
	}
	allowed, err = policy.Allowed(context.Background(), "user-1", "calculator")
	if err != nil || !allowed {
		t.Fatalf("allowed = %v, err = %v", allowed, err)
	}
	adminPolicy := NewRolePolicy(stubRoleLookup{admin: true}, []string{"admin_tool"})
	allowed, err = adminPolicy.Allowed(context.Background(), "user-1", "admin_tool")
	if err != nil || !allowed {
		t.Fatalf("allowed = %v, err = %v", allowed, err)
	}
}
