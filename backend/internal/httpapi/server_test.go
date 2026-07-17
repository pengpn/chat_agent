package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/example/agent-platform/backend/internal/config"
	"github.com/example/agent-platform/backend/internal/repository"
	"github.com/gin-gonic/gin"
)

func TestRouterHealthCheck(t *testing.T) {
	gin.SetMode(gin.TestMode)
	server := NewServer(repository.New(nil), nil, nil, config.Config{})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)

	server.Router().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if recorder.Body.String() != "{\"code\":0,\"msg\":\"ok\",\"data\":{\"status\":\"ok\"}}" {
		t.Fatalf("body = %q", recorder.Body.String())
	}
}

func TestRouterReturnsAndPropagatesRequestID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	server := NewServer(repository.New(nil), nil, nil, config.Config{})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	request.Header.Set("X-Request-ID", "trace-2026.07")

	server.Router().ServeHTTP(recorder, request)

	if got := recorder.Header().Get("X-Request-ID"); got != "trace-2026.07" {
		t.Fatalf("request id = %q", got)
	}
	if got := recorder.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Fatalf("content type = %q", got)
	}
}

func TestRouterHandlesCORSPreflight(t *testing.T) {
	gin.SetMode(gin.TestMode)
	server := NewServer(repository.New(nil), nil, nil, config.Config{CORSAllowedOrigins: []string{"http://localhost:5173"}})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodOptions, "/api/v1/conversations", nil)
	request.Header.Set("Origin", "http://localhost:5173")

	server.Router().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Fatalf("allow origin = %q", got)
	}
	if got := recorder.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(got, "X-Request-ID") {
		t.Fatalf("allow headers = %q", got)
	}
}
