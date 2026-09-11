package embeddinggateway

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

type CachedEmbedding struct {
	Values    []float32 `json:"values"`
	Dimension int       `json:"dimension"`
}

// Cache stores only embeddings, never original text. Gateway cache keys are
// hashes scoped to user, provider, model and dimension.
type Cache interface {
	Get(ctx context.Context, key string) (CachedEmbedding, bool, error)
	Set(ctx context.Context, key string, value CachedEmbedding) error
}

type RedisCache struct {
	client redis.UniversalClient
	prefix string
	ttl    time.Duration
}

func NewRedisCache(client redis.UniversalClient, prefix string, ttl time.Duration) *RedisCache {
	if prefix == "" {
		prefix = "agent-platform:embedding:cache:"
	}
	if ttl <= 0 {
		ttl = 30 * 24 * time.Hour
	}
	return &RedisCache{client: client, prefix: prefix, ttl: ttl}
}

func (c *RedisCache) Get(ctx context.Context, key string) (CachedEmbedding, bool, error) {
	if c == nil || c.client == nil {
		return CachedEmbedding{}, false, nil
	}
	payload, err := c.client.Get(ctx, c.prefix+key).Bytes()
	if err == redis.Nil {
		return CachedEmbedding{}, false, nil
	}
	if err != nil {
		return CachedEmbedding{}, false, fmt.Errorf("read embedding cache: %w", err)
	}
	var value CachedEmbedding
	if err := json.Unmarshal(payload, &value); err != nil {
		return CachedEmbedding{}, false, fmt.Errorf("decode embedding cache: %w", err)
	}
	return value, true, nil
}

func (c *RedisCache) Set(ctx context.Context, key string, value CachedEmbedding) error {
	if c == nil || c.client == nil {
		return nil
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode embedding cache: %w", err)
	}
	if err := c.client.Set(ctx, c.prefix+key, payload, c.ttl).Err(); err != nil {
		return fmt.Errorf("write embedding cache: %w", err)
	}
	return nil
}
