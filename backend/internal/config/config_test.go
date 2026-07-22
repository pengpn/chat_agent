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
