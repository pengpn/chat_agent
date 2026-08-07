package config

import (
	"testing"
	"time"
)

func TestParseModelPricing(t *testing.T) {
	pricing, err := parseModelPricing("model-a:0.15:0.60,model-b:1.2:4.8")
	if err != nil {
		t.Fatal(err)
	}
	if got := pricing["model-a"]; got.InputUSDPerMillionTokens != 0.15 || got.OutputUSDPerMillionTokens != 0.60 {
		t.Fatalf("model-a pricing = %#v", got)
	}
	if _, err := parseModelPricing("missing-output:0.15"); err == nil {
		t.Fatal("expected invalid pricing to fail")
	}
}

func TestParseModelLimits(t *testing.T) {
	limits, err := parseModelLimits("model-a:128000:8192", []string{"model-a", "model-b"}, ModelLimits{ContextWindow: 32768, MaxOutputTokens: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if limits["model-a"].ContextWindow != 128000 || limits["model-a"].MaxOutputTokens != 8192 {
		t.Fatalf("model-a limits = %#v", limits["model-a"])
	}
	if limits["model-b"].ContextWindow != 32768 || limits["model-b"].MaxOutputTokens != 4096 {
		t.Fatalf("model-b defaults = %#v", limits["model-b"])
	}
	for _, value := range []string{"model-a:100", "model-a:100:100", "disabled:100:10"} {
		if _, err := parseModelLimits(value, []string{"model-a"}, ModelLimits{ContextWindow: 100, MaxOutputTokens: 10}); err == nil {
			t.Fatalf("parseModelLimits(%q) unexpectedly succeeded", value)
		}
	}
	if _, err := parseModelLimits("", []string{"model-a"}, ModelLimits{ContextWindow: 100, MaxOutputTokens: 100}); err == nil {
		t.Fatal("expected invalid default limits to fail")
	}
}

func TestParseModelTokenizers(t *testing.T) {
	defaults := map[string]string{"model-a": "heuristic", "model-b": "heuristic"}
	tokenizers, err := parseModelTokenizers("model-a:o200k_base,model-b:deepseek_v4", []string{"model-a", "model-b"}, defaults)
	if err != nil {
		t.Fatal(err)
	}
	if tokenizers["model-a"] != "o200k_base" || tokenizers["model-b"] != "deepseek_v4" {
		t.Fatalf("tokenizers = %#v", tokenizers)
	}
	for _, value := range []string{"model-a:unknown", "disabled:o200k_base", "missing-separator"} {
		if _, err := parseModelTokenizers(value, []string{"model-a"}, map[string]string{"model-a": "heuristic"}); err == nil {
			t.Fatalf("parseModelTokenizers(%q) unexpectedly succeeded", value)
		}
	}
	if defaultOpenAITokenizer("gpt-4o-mini") != "o200k_base" || defaultOpenAITokenizer("gpt-4.1-mini") != "o200k_base" || defaultOpenAITokenizer("gpt-4-turbo") != "cl100k_base" || defaultOpenAITokenizer("custom") != "heuristic" {
		t.Fatal("unexpected OpenAI tokenizer inference")
	}
}

func TestValidateSummaryTarget(t *testing.T) {
	provider := ProviderConfig{ID: "openai", APIKey: "key", Models: []string{"small"}}
	valid := Config{ContextSummaryEnabled: true, ContextSummaryProvider: "openai", ContextSummaryModel: "small", Providers: []ProviderConfig{provider}}
	if err := validateSummaryTarget(valid); err != nil {
		t.Fatal(err)
	}
	invalid := valid
	invalid.ContextSummaryModel = "missing"
	if err := validateSummaryTarget(invalid); err == nil {
		t.Fatal("expected an unavailable summary model to fail")
	}
	invalid = valid
	invalid.ContextSummaryModel = ""
	if err := validateSummaryTarget(invalid); err == nil {
		t.Fatal("expected a partial summary target to fail")
	}
}

func TestValidateOptionalModelTarget(t *testing.T) {
	providers := []ProviderConfig{{ID: "deepseek", APIKey: "key", Models: []string{"deepseek-chat"}}}
	if err := validateOptionalModelTarget("TITLE_GENERATION", "deepseek", "deepseek-chat", providers); err != nil {
		t.Fatal(err)
	}
	for _, target := range [][2]string{{"deepseek", ""}, {"", "deepseek-chat"}, {"deepseek", "missing"}, {"openai", "gpt"}} {
		if err := validateOptionalModelTarget("TITLE_GENERATION", target[0], target[1], providers); err == nil {
			t.Fatalf("target %#v unexpectedly passed", target)
		}
	}
}

func TestStreamConfigurationParsers(t *testing.T) {
	t.Setenv("TEST_STREAM_TIMEOUT", "1500ms")
	if timeout, err := positiveDurationEnv("TEST_STREAM_TIMEOUT", "2m"); err != nil || timeout != 1500*time.Millisecond {
		t.Fatalf("timeout=%s err=%v", timeout, err)
	}
}

func TestValidateThinkingBudgetTokens(t *testing.T) {
	for _, value := range []int{1024, 2048, 4095} {
		if err := validateThinkingBudgetTokens(value); err != nil {
			t.Errorf("validateThinkingBudgetTokens(%d) = %v", value, err)
		}
	}
	for _, value := range []int{0, 1023, 4096} {
		if err := validateThinkingBudgetTokens(value); err == nil {
			t.Errorf("validateThinkingBudgetTokens(%d) unexpectedly succeeded", value)
		}
	}
}
