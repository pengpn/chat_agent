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
