package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/example/agent-platform/backend/internal/auth"
	"github.com/gin-gonic/gin"
)

type sessionValidatorStub struct{ active bool }

func (s sessionValidatorStub) SessionActive(context.Context, string, string) (bool, error) {
	return s.active, nil
}

func TestAuthenticateRejectsRevokedSessionBeforeAccessTokenExpires(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := auth.NewJWTManager("test-secret", time.Hour)
	raw, _, err := manager.Issue("user-1", "demo@example.com", "session-1")
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.Use(Authenticate(manager, sessionValidatorStub{active: false}))
	router.GET("/protected", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.Header.Set("Authorization", "Bearer "+raw)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", response.Code)
	}
}
