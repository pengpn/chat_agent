package tool

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/example/agent-platform/backend/internal/provider"
)

func TestCalculatorEvaluatesArithmeticWithoutExecutingCode(t *testing.T) {
	calculator := NewCalculator()
	output, err := calculator.Execute(context.Background(), `{"expression":"(2 + 3) * 4 - 1"}`)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Result float64 `json:"result"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	if result.Result != 19 {
		t.Fatalf("result = %v, want 19", result.Result)
	}
}

func TestCalculatorRejectsUnsafeOrInvalidExpressions(t *testing.T) {
	calculator := NewCalculator()
	for _, arguments := range []string{
		`{"expression":"1 / 0"}`,
		`{"expression":"os.Exit(1)"}`,
		`{"expression":""}`,
		`{"expression":"2 + 2", "unexpected":true}`,
	} {
		if _, err := calculator.Execute(context.Background(), arguments); err == nil {
			t.Fatalf("arguments %s should fail", arguments)
		}
	}
}

func TestRegistryRejectsToolCallsWithoutAnID(t *testing.T) {
	registry := NewDefaultRegistry()
	if _, err := registry.Execute(context.Background(), provider.ToolCall{Function: provider.ToolFunctionCall{Name: "calculator", Arguments: `{"expression":"1+1"}`}}); err == nil {
		t.Fatal("tool call without id should fail")
	}
}
