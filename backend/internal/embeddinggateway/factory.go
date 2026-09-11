package embeddinggateway

import (
	"github.com/example/agent-platform/backend/internal/config"
	"github.com/example/agent-platform/backend/internal/provider"
	"github.com/redis/go-redis/v9"
)

// NewConfigured assembles the production gateway without exposing provider
// protocol details to the service layer. Disabled configuration returns nil so
// the next ingestion stage can report that RAG is unavailable.
func NewConfigured(cfg config.EmbeddingConfig, redisClient redis.UniversalClient) (Gateway, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	estimator, err := NewTokenEstimator(cfg.Tokenizer)
	if err != nil {
		return nil, err
	}
	client := provider.NewOpenAICompatibleEmbedding(cfg)
	var cache Cache
	if redisClient != nil {
		cache = NewRedisCache(redisClient, "", cfg.CacheTTL)
	}
	return New(client, estimator, cache, nil, Config{
		Provider: cfg.Provider, Model: cfg.Model, Dimension: cfg.Dimension, SendDimensions: cfg.SendDimensions,
		MaxInputs: cfg.MaxInputs, BatchSize: cfg.BatchSize,
		MaxInputTokens: cfg.MaxInputTokens, MaxBatchTokens: cfg.MaxBatchTokens,
		RequestTimeout: cfg.RequestTimeout, MaxRetries: cfg.MaxRetries, RetryBackoff: cfg.RetryBackoff,
		InputUSDPerMillionTokens: cfg.InputUSDPerMillionTokens,
	}), nil
}
