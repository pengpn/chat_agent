// Package ratelimit provides shared, Redis-backed request admission controls.
package ratelimit

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

type Decision struct {
	Allowed    bool
	RetryAfter time.Duration
}

type Limiter interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) (Decision, error)
}

type Noop struct{}

func (Noop) Allow(context.Context, string, int, time.Duration) (Decision, error) {
	return Decision{Allowed: true}, nil
}

type RedisLimiter struct {
	client *redis.Client
	prefix string
}

func NewRedis(client *redis.Client, prefix string) *RedisLimiter {
	if prefix == "" {
		prefix = "agent:rate:"
	}
	return &RedisLimiter{client: client, prefix: prefix}
}

var fixedWindowScript = redis.NewScript(`
local count = redis.call('INCR', KEYS[1])
if count == 1 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
local ttl = redis.call('PTTL', KEYS[1])
return {count, ttl}
`)

func (l *RedisLimiter) Allow(ctx context.Context, key string, limit int, window time.Duration) (Decision, error) {
	if limit <= 0 {
		return Decision{Allowed: true}, nil
	}
	values, err := fixedWindowScript.Run(ctx, l.client, []string{l.prefix + key}, window.Milliseconds()).Int64Slice()
	if err != nil {
		return Decision{}, err
	}
	retryAfter := time.Duration(values[1]) * time.Millisecond
	if retryAfter < 0 {
		retryAfter = 0
	}
	return Decision{Allowed: values[0] <= int64(limit), RetryAfter: retryAfter}, nil
}
