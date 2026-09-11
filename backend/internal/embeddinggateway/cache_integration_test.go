package embeddinggateway

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestRedisCacheRoundTrip(t *testing.T) {
	redisURL := os.Getenv("TEST_REDIS_URL")
	if redisURL == "" {
		t.Skip("TEST_REDIS_URL is not configured")
	}
	options, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(options)
	defer client.Close()
	ctx := context.Background()
	prefix := fmt.Sprintf("agent-platform:test:embedding:%d:", time.Now().UnixNano())
	key := "content-key"
	defer client.Del(ctx, prefix+key)
	cache := NewRedisCache(client, prefix, time.Minute)
	want := CachedEmbedding{Values: []float32{0.1, 0.2, 0.3}, Dimension: 3}
	if err := cache.Set(ctx, key, want); err != nil {
		t.Fatal(err)
	}
	got, found, err := cache.Get(ctx, key)
	if err != nil || !found || got.Dimension != 3 || len(got.Values) != 3 || got.Values[1] != want.Values[1] {
		t.Fatalf("got=%#v found=%t err=%v", got, found, err)
	}
	if ttl, err := client.TTL(ctx, prefix+key).Result(); err != nil || ttl <= 0 {
		t.Fatalf("ttl=%s err=%v", ttl, err)
	}
}
