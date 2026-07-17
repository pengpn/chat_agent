package database

import (
	"context"
	"testing"
)

func TestWithDefaults(t *testing.T) {
	config := withDefaults(Config{URL: "postgres://example"})
	if config.MaxOpenConns != defaultMaxOpenConns {
		t.Fatalf("MaxOpenConns = %d, want %d", config.MaxOpenConns, defaultMaxOpenConns)
	}
	if config.MaxIdleConns != defaultMaxIdleConns {
		t.Fatalf("MaxIdleConns = %d, want %d", config.MaxIdleConns, defaultMaxIdleConns)
	}
	if config.ConnMaxLifetime != defaultConnMaxLifetime {
		t.Fatalf("ConnMaxLifetime = %s, want %s", config.ConnMaxLifetime, defaultConnMaxLifetime)
	}
	if config.ConnMaxIdleTime != defaultConnMaxIdleTime {
		t.Fatalf("ConnMaxIdleTime = %s, want %s", config.ConnMaxIdleTime, defaultConnMaxIdleTime)
	}
}

func TestOpenRejectsEmptyURL(t *testing.T) {
	if _, _, err := Open(context.Background(), Config{}); err == nil {
		t.Fatal("Open() error = nil, want error")
	}
}
