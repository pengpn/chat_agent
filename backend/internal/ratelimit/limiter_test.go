package ratelimit

import (
	"context"
	"testing"
	"time"
)

func TestNoopAlwaysAllows(t *testing.T) {
	decision, err := (Noop{}).Allow(context.Background(), "login", 1, time.Minute)
	if err != nil || !decision.Allowed {
		t.Fatalf("Allow() = %#v, %v", decision, err)
	}
}
