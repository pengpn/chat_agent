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
	ID      string   `json:"id"`
	Label   string   `json:"label"`
	Type    string   `json:"type"`
	BaseURL string   `json:"baseUrl"`
	APIKey  string   `json:"-"`
	Models  []string `json:"models"`
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
		SystemPrompt:            env("SYSTEM_PROMPT", "You are a helpful AI assistant."),
		Providers: []ProviderConfig{
			provider("openai", "OpenAI", "openai-compatible", "OPENAI", "https://api.openai.com/v1", "gpt-4o-mini"),
			provider("kimi", "Kimi", "openai-compatible", "KIMI", "https://api.moonshot.cn/v1", "moonshot-v1-8k"),
			provider("anthropic", "Anthropic", "anthropic", "ANTHROPIC", "https://api.anthropic.com/v1", "claude-sonnet-4-20250514"),
			provider("deepseek", "DeepSeek", "openai-compatible", "DEEPSEEK", "https://api.deepseek.com/v1", "deepseek-chat"),
			provider("glm", "GLM", "openai-compatible", "GLM", "https://open.bigmodel.cn/api/paas/v4", "glm-4-flash"),
		},
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

func provider(id, label, kind, prefix, defaultBaseURL, defaultModels string) ProviderConfig {
	return ProviderConfig{
		ID:      id,
		Label:   label,
		Type:    kind,
		BaseURL: strings.TrimRight(env(prefix+"_BASE_URL", defaultBaseURL), "/"),
		APIKey:  os.Getenv(prefix + "_API_KEY"),
		Models:  splitCSV(env(prefix+"_MODELS", defaultModels)),
	}
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
