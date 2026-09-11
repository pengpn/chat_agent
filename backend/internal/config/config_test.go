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

func TestLoadEmbeddingConfig(t *testing.T) {
	setEmbeddingTestEnv(t)
	t.Setenv("EMBEDDING_ENABLED", "true")
	t.Setenv("EMBEDDING_API_KEY", "test-key")
	t.Setenv("EMBEDDING_PROVIDER", "qwen")
	t.Setenv("EMBEDDING_BASE_URL", "https://example.com/v1/")
	t.Setenv("EMBEDDING_MODEL", "embedding-demo")
	t.Setenv("EMBEDDING_DIMENSION", "1024")
	t.Setenv("EMBEDDING_INPUT_USD_PER_MILLION_TOKENS", "0.02")

	cfg, err := loadEmbeddingConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Enabled || cfg.Provider != "qwen" || cfg.BaseURL != "https://example.com/v1" || cfg.Model != "embedding-demo" || cfg.Dimension != 1024 {
		t.Fatalf("config = %#v", cfg)
	}
	if cfg.InputUSDPerMillionTokens == nil || *cfg.InputUSDPerMillionTokens != 0.02 {
		t.Fatalf("price = %#v", cfg.InputUSDPerMillionTokens)
	}
}

func TestLoadEmbeddingConfigValidation(t *testing.T) {
	t.Run("enabled requires API key", func(t *testing.T) {
		setEmbeddingTestEnv(t)
		t.Setenv("EMBEDDING_ENABLED", "true")
		if _, err := loadEmbeddingConfig(); err == nil {
			t.Fatal("expected missing API key to fail")
		}
	})
	t.Run("batch size cannot exceed max inputs", func(t *testing.T) {
		setEmbeddingTestEnv(t)
		t.Setenv("EMBEDDING_MAX_INPUTS", "2")
		t.Setenv("EMBEDDING_BATCH_SIZE", "3")
		if _, err := loadEmbeddingConfig(); err == nil {
			t.Fatal("expected invalid batch size to fail")
		}
	})
	t.Run("tokenizer must be supported", func(t *testing.T) {
		setEmbeddingTestEnv(t)
		t.Setenv("EMBEDDING_TOKENIZER", "unknown")
		if _, err := loadEmbeddingConfig(); err == nil {
			t.Fatal("expected invalid tokenizer to fail")
		}
	})
}

func setEmbeddingTestEnv(t *testing.T) {
	t.Helper()
	values := map[string]string{
		"EMBEDDING_ENABLED":                      "false",
		"EMBEDDING_PROVIDER":                     "openai",
		"EMBEDDING_BASE_URL":                     "https://api.openai.com/v1",
		"EMBEDDING_API_KEY":                      "",
		"EMBEDDING_MODEL":                        "text-embedding-3-small",
		"EMBEDDING_DIMENSION":                    "1536",
		"EMBEDDING_SEND_DIMENSIONS":              "true",
		"EMBEDDING_TOKENIZER":                    "cl100k_base",
		"EMBEDDING_MAX_INPUTS":                   "2048",
		"EMBEDDING_BATCH_SIZE":                   "64",
		"EMBEDDING_MAX_INPUT_TOKENS":             "8191",
		"EMBEDDING_MAX_BATCH_TOKENS":             "100000",
		"EMBEDDING_CONNECT_TIMEOUT":              "10s",
		"EMBEDDING_REQUEST_TIMEOUT":              "30s",
		"EMBEDDING_MAX_RETRIES":                  "2",
		"EMBEDDING_RETRY_BACKOFF":                "300ms",
		"EMBEDDING_CACHE_TTL":                    "720h",
		"EMBEDDING_INPUT_USD_PER_MILLION_TOKENS": "",
	}
	for key, value := range values {
		t.Setenv(key, value)
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

func TestConfiguredProvidersIncludesQwenVisionModel(t *testing.T) {
	t.Setenv("QWEN_MODELS", "qwen3-vl-flash")
	t.Setenv("QWEN_VISION_MODELS", "qwen3-vl-flash")
	t.Setenv("QWEN_MODEL_LIMITS", "qwen3-vl-flash:262144:32768")
	providers, err := configuredProviders(10*time.Second, 32768, 4096)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range providers {
		if item.ID != "qwen" {
			continue
		}
		if item.BaseURL != "https://dashscope.aliyuncs.com/compatible-mode/v1" {
			t.Fatalf("unexpected base URL: %s", item.BaseURL)
		}
		if !item.VisionModels["qwen3-vl-flash"] {
			t.Fatal("qwen3-vl-flash should be a vision model")
		}
		limits := item.ModelLimits["qwen3-vl-flash"]
		if limits.ContextWindow != 262144 || limits.MaxOutputTokens != 32768 {
			t.Fatalf("unexpected limits: %+v", limits)
		}
		return
	}
	t.Fatal("qwen provider not found")
}

func TestStreamConfigurationParsers(t *testing.T) {
	t.Setenv("TEST_STREAM_TIMEOUT", "1500ms")
	if timeout, err := positiveDurationEnv("TEST_STREAM_TIMEOUT", "2m"); err != nil || timeout != 1500*time.Millisecond {
		t.Fatalf("timeout=%s err=%v", timeout, err)
	}
}

func TestLoadKnowledgeConfigUsesTokenUnitsAndValidatesOverlap(t *testing.T) {
	t.Setenv("RAG_CHUNK_TARGET_TOKENS", "600")
	t.Setenv("RAG_CHUNK_OVERLAP_TOKENS", "90")
	t.Setenv("RAG_CHUNK_MIN_TOKENS", "50")
	config, err := loadKnowledgeConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.ChunkTargetTokens != 600 || config.ChunkOverlap != 90 || config.ChunkMinTokens != 50 {
		t.Fatalf("unexpected knowledge config: %+v", config)
	}
	t.Setenv("RAG_CHUNK_OVERLAP_TOKENS", "600")
	if _, err := loadKnowledgeConfig(); err == nil {
		t.Fatal("expected overlap equal to target to fail")
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
