package config

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// RerankConfig 独立配置重排模型，避免与对话模型、Embedding 模型混用。
type RerankConfig struct {
	Enabled        bool
	Provider       string
	Endpoint       string
	APIKey         string
	Model          string
	Tokenizer      string
	MaxDocuments   int
	MaxPairTokens  int64
	MaxTotalTokens int64
	ConnectTimeout time.Duration
	RequestTimeout time.Duration
}

func loadRerankConfig() (RerankConfig, error) {
	enabled, err := boolEnv("RERANK_ENABLED", false)
	if err != nil {
		return RerankConfig{}, err
	}
	maxDocuments, err := positiveIntEnv("RERANK_MAX_DOCUMENTS", 60)
	if err != nil {
		return RerankConfig{}, err
	}
	if maxDocuments > 400 {
		return RerankConfig{}, fmt.Errorf("RERANK_MAX_DOCUMENTS must not exceed 400")
	}
	maxPair, err := positiveInt64Env("RERANK_MAX_PAIR_TOKENS", 4096)
	if err != nil {
		return RerankConfig{}, err
	}
	maxTotal, err := positiveInt64Env("RERANK_MAX_TOTAL_TOKENS", 32768)
	if err != nil {
		return RerankConfig{}, err
	}
	connectTimeout, err := positiveDurationEnv("RERANK_CONNECT_TIMEOUT", "5s")
	if err != nil {
		return RerankConfig{}, err
	}
	requestTimeout, err := positiveDurationEnv("RERANK_REQUEST_TIMEOUT", "15s")
	if err != nil {
		return RerankConfig{}, err
	}
	cfg := RerankConfig{
		Enabled: enabled, Provider: strings.TrimSpace(env("RERANK_PROVIDER", "compatible")),
		Endpoint: strings.TrimSpace(env("RERANK_ENDPOINT", "")), APIKey: env("RERANK_API_KEY", ""),
		Model: strings.TrimSpace(env("RERANK_MODEL", "")), Tokenizer: strings.TrimSpace(env("RERANK_TOKENIZER", "heuristic")),
		MaxDocuments: maxDocuments, MaxPairTokens: maxPair, MaxTotalTokens: maxTotal,
		ConnectTimeout: connectTimeout, RequestTimeout: requestTimeout,
	}
	if !supportedEmbeddingTokenizer(cfg.Tokenizer) {
		return RerankConfig{}, fmt.Errorf("unsupported RERANK_TOKENIZER %q", cfg.Tokenizer)
	}
	if enabled {
		endpoint, parseErr := url.Parse(cfg.Endpoint)
		if parseErr != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
			return RerankConfig{}, fmt.Errorf("RERANK_ENDPOINT must be a complete HTTP(S) rerank URL without credentials, query or fragment")
		}
		if cfg.Provider == "" || cfg.Model == "" {
			return RerankConfig{}, fmt.Errorf("RERANK_PROVIDER and RERANK_MODEL are required when RERANK_ENABLED=true")
		}
	}
	return cfg, nil
}
