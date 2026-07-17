package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type ProviderConfig struct {
	ID                 string                  `json:"id"`
	Label              string                  `json:"label"`
	Type               string                  `json:"type"`
	BaseURL            string                  `json:"baseUrl"`
	APIKey             string                  `json:"-"`
	Models             []string                `json:"models"`
	StreamIncludeUsage bool                    `json:"-"`
	ModelPricing       map[string]ModelPricing `json:"-"`
}

// ModelPricing uses USD per one million tokens. It intentionally lives in
// configuration because providers report usage while deployment owners define
// the prices they are billed.
type ModelPricing struct {
	InputUSDPerMillionTokens  float64
	OutputUSDPerMillionTokens float64
}

type Config struct {
	HTTPAddr                string
	DatabaseURL             string
	DatabaseMaxOpenConns    int
	DatabaseMaxIdleConns    int
	DatabaseConnMaxLifetime time.Duration
	DatabaseConnMaxIdleTime time.Duration
	JWTSecret               string
	JWTTTL                  time.Duration
	CORSAllowedOrigins      []string
	LogLevel                string
	MaxToolIterations       int
	EnableThinking          bool
	ThinkingBudgetTokens    int
	SystemPrompt            string
	Providers               []ProviderConfig
}

func Load() (Config, error) {
	// Support the root .env file for local development while keeping real
	// environment variables authoritative in containers and production.
	for _, path := range []string{".env", "../.env"} {
		if err := loadDotEnv(path); err != nil {
			return Config{}, err
		}
	}

	ttl, err := time.ParseDuration(env("JWT_TTL_HOURS", "168") + "h")
	if err != nil {
		return Config{}, fmt.Errorf("parse JWT_TTL_HOURS: %w", err)
	}
	databaseMaxOpenConns, err := positiveIntEnv("DATABASE_MAX_OPEN_CONNS", 20)
	if err != nil {
		return Config{}, err
	}
	databaseMaxIdleConns, err := positiveIntEnv("DATABASE_MAX_IDLE_CONNS", 10)
	if err != nil {
		return Config{}, err
	}
	databaseConnMaxLifetime, err := time.ParseDuration(env("DATABASE_CONN_MAX_LIFETIME", "1h"))
	if err != nil {
		return Config{}, fmt.Errorf("parse DATABASE_CONN_MAX_LIFETIME: %w", err)
	}
	databaseConnMaxIdleTime, err := time.ParseDuration(env("DATABASE_CONN_MAX_IDLE_TIME", "10m"))
	if err != nil {
		return Config{}, fmt.Errorf("parse DATABASE_CONN_MAX_IDLE_TIME: %w", err)
	}
	maxToolIterations, err := positiveIntEnv("MAX_TOOL_ITERATIONS", 4)
	if err != nil {
		return Config{}, err
	}
	if maxToolIterations > 10 {
		return Config{}, fmt.Errorf("MAX_TOOL_ITERATIONS must not exceed 10")
	}
	enableThinking, err := boolEnv("ENABLE_THINKING", false)
	if err != nil {
		return Config{}, err
	}
	thinkingBudgetTokens, err := positiveIntEnv("THINKING_BUDGET_TOKENS", 2048)
	if err != nil {
		return Config{}, err
	}
	if err := validateThinkingBudgetTokens(thinkingBudgetTokens); err != nil {
		return Config{}, err
	}

	providers, err := configuredProviders()
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		HTTPAddr:                env("HTTP_ADDR", ":8080"),
		DatabaseURL:             env("DATABASE_URL", "postgres://agent:agent_dev_password@localhost:5432/agent_platform?sslmode=disable"),
		DatabaseMaxOpenConns:    databaseMaxOpenConns,
		DatabaseMaxIdleConns:    databaseMaxIdleConns,
		DatabaseConnMaxLifetime: databaseConnMaxLifetime,
		DatabaseConnMaxIdleTime: databaseConnMaxIdleTime,
		JWTSecret:               os.Getenv("JWT_SECRET"),
		JWTTTL:                  ttl,
		CORSAllowedOrigins:      splitCSV(env("CORS_ALLOWED_ORIGINS", "http://localhost:5173")),
		LogLevel:                env("LOG_LEVEL", "INFO"),
		MaxToolIterations:       maxToolIterations,
		EnableThinking:          enableThinking,
		ThinkingBudgetTokens:    thinkingBudgetTokens,
		SystemPrompt:            env("SYSTEM_PROMPT", "You are a helpful AI assistant."),
		Providers:               providers,
	}
	if len(cfg.JWTSecret) < 32 {
		return Config{}, fmt.Errorf("JWT_SECRET must contain at least 32 characters")
	}

	configured := make([]ProviderConfig, 0, len(cfg.Providers))
	for _, p := range cfg.Providers {
		if p.APIKey != "" {
			configured = append(configured, p)
		}
	}
	cfg.Providers = configured
	return cfg, nil
}

func configuredProviders() ([]ProviderConfig, error) {
	type providerDefinition struct {
		id, label, kind, prefix, baseURL, models string
	}
	definitions := []providerDefinition{
		{"openai", "OpenAI", "openai-compatible", "OPENAI", "https://api.openai.com/v1", "gpt-4o-mini"},
		{"kimi", "Kimi", "openai-compatible", "KIMI", "https://api.moonshot.cn/v1", "moonshot-v1-8k"},
		{"anthropic", "Anthropic", "anthropic", "ANTHROPIC", "https://api.anthropic.com/v1", "claude-sonnet-4-20250514"},
		{"deepseek", "DeepSeek", "openai-compatible", "DEEPSEEK", "https://api.deepseek.com/v1", "deepseek-chat"},
		{"glm", "GLM", "openai-compatible", "GLM", "https://open.bigmodel.cn/api/paas/v4", "glm-4-flash"},
	}
	result := make([]ProviderConfig, 0, len(definitions))
	for _, definition := range definitions {
		pricing, err := parseModelPricing(env(definition.prefix+"_MODEL_PRICING", ""))
		if err != nil {
			return nil, fmt.Errorf("parse %s_MODEL_PRICING: %w", definition.prefix, err)
		}
		includeUsage, err := boolEnv(definition.prefix+"_STREAM_INCLUDE_USAGE", true)
		if err != nil {
			return nil, err
		}
		result = append(result, ProviderConfig{
			ID:                 definition.id,
			Label:              definition.label,
			Type:               definition.kind,
			BaseURL:            strings.TrimRight(env(definition.prefix+"_BASE_URL", definition.baseURL), "/"),
			APIKey:             os.Getenv(definition.prefix + "_API_KEY"),
			Models:             splitCSV(env(definition.prefix+"_MODELS", definition.models)),
			StreamIncludeUsage: includeUsage,
			ModelPricing:       pricing,
		})
	}
	return result, nil
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func positiveIntEnv(key string, fallback int) (int, error) {
	value := env(key, fmt.Sprintf("%d", fallback))
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	return parsed, nil
}

func validateThinkingBudgetTokens(value int) error {
	// Anthropic requires at least 1024 thinking tokens and the current adapter
	// uses max_tokens=4096, so keep the budget inside that supported range.
	if value < 1024 || value >= 4096 {
		return fmt.Errorf("THINKING_BUDGET_TOKENS must be between 1024 and 4095")
	}
	return nil
}

func boolEnv(key string, fallback bool) (bool, error) {
	value := env(key, strconv.FormatBool(fallback))
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean: %w", key, err)
	}
	return parsed, nil
}

// parseModelPricing accepts comma-separated entries in the form
// model:input_usd_per_million:output_usd_per_million.
func parseModelPricing(value string) (map[string]ModelPricing, error) {
	pricing := make(map[string]ModelPricing)
	if strings.TrimSpace(value) == "" {
		return pricing, nil
	}
	for _, item := range splitCSV(value) {
		parts := strings.Split(item, ":")
		if len(parts) != 3 || strings.TrimSpace(parts[0]) == "" {
			return nil, fmt.Errorf("%q must be model:input:output", item)
		}
		input, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
		if err != nil || input < 0 {
			return nil, fmt.Errorf("%q has an invalid input price", item)
		}
		output, err := strconv.ParseFloat(strings.TrimSpace(parts[2]), 64)
		if err != nil || output < 0 {
			return nil, fmt.Errorf("%q has an invalid output price", item)
		}
		pricing[strings.TrimSpace(parts[0])] = ModelPricing{InputUSDPerMillionTokens: input, OutputUSDPerMillionTokens: output}
	}
	return pricing, nil
}

func splitCSV(value string) []string {
	items := strings.Split(value, ",")
	result := make([]string, 0, len(items))
	for _, item := range items {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}

func loadDotEnv(path string) error {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return fmt.Errorf("parse %s: expected KEY=VALUE", path)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
			value = value[1 : len(value)-1]
		}
		if _, exists := os.LookupEnv(key); !exists {
			if err := os.Setenv(key, value); err != nil {
				return fmt.Errorf("set %s from %s: %w", key, path, err)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	return nil
}
