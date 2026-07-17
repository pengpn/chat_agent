package config

import "testing"

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
