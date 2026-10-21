package main

import (
	"context"
	"time"

	"github.com/example/agent-platform/backend/internal/config"
	"github.com/example/agent-platform/backend/internal/llmgateway"
	"github.com/example/agent-platform/backend/internal/provider"
	"github.com/example/agent-platform/backend/internal/service"
	"github.com/redis/go-redis/v9"
)

// newQueryRewriter 为在线抓取复用正式网关；开启限流时也复用相同 Redis 桶，不能靠 CLI 绕过额度。
// evaluate 和 sweep 只读快照，不会创建此网关或调用模型。
func newQueryRewriter(ctx context.Context, cfg config.Config) (*service.QueryRewriter, func(), error) {
	cleanup := func() {}
	if !cfg.QueryRewrite.Enabled {
		return service.NewQueryRewriter(nil, cfg.QueryRewrite), cleanup, nil
	}
	var limiter llmgateway.RateLimiter
	if cfg.LLMRateTokensPerMinute > 0 {
		options, err := redis.ParseURL(cfg.RedisURL)
		if err != nil {
			return nil, cleanup, err
		}
		client := redis.NewClient(options)
		pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err = client.Ping(pingCtx).Err()
		cancel()
		if err != nil {
			_ = client.Close()
			return nil, cleanup, err
		}
		cleanup = func() { _ = client.Close() }
		limiter = llmgateway.NewTokenBucketLimiter(client, cfg.LLMRateTokensPerMinute, cfg.LLMRateBurst, "")
	}
	providers := provider.NewRegistry(cfg.Providers)
	gateway := llmgateway.New(providers, llmgateway.NewModelTokenEstimator(providers), limiter,
		llmgateway.NewMemoryIdempotencyStore(cfg.LLMIdempotencyTTL), llmgateway.ZapObservation{}, llmgateway.Config{
			TokenBudget: cfg.LLMTokenBudget, MaxOutputTokens: cfg.LLMMaxOutputTokens,
			SyncMaxRetries: cfg.LLMSyncMaxRetries, RetryBackoff: cfg.LLMRetryBackoff,
		})
	return service.NewQueryRewriter(gateway, cfg.QueryRewrite), cleanup, nil
}
