package auth

import (
	"testing"
	"time"
)

func TestOpaqueTokenIsRandomAndHashIsDeterministic(t *testing.T) {
	first, err := NewOpaqueToken()
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewOpaqueToken()
	if err != nil {
		t.Fatal(err)
	}
	if first == second || len(first) < 40 {
		t.Fatal("opaque tokens must contain independent high-entropy values")
	}
	if HashOpaqueToken(first) != HashOpaqueToken(first) || HashOpaqueToken(first) == first {
		t.Fatal("opaque token hash must be deterministic and differ from plaintext")
	}
}

func TestJWTIncludesDeviceSessionID(t *testing.T) {
	manager := NewJWTManager("test-secret", time.Hour)
	raw, _, err := manager.Issue("user-1", "demo@example.com", "session-1")
	if err != nil {
		t.Fatal(err)
	}
	claims, err := manager.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Subject != "user-1" || claims.SessionID != "session-1" {
		t.Fatalf("claims = %+v", claims)
	}
}
