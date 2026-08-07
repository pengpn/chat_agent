package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/example/agent-platform/backend/internal/httpapi/middleware"
	"github.com/example/agent-platform/backend/internal/userevent"
	"github.com/gin-gonic/gin"
)

func TestUserEventsStreamsOnlyAuthenticatedUsersEvents(t *testing.T) {
	gin.SetMode(gin.TestMode)
	hub := userevent.New(time.Minute, 10)
	hub.Publish("user-1", "conversation.title.updated", map[string]any{"title": "Agent 学习"})
	handler := &Handler{events: hub}
	engine := gin.New()
	engine.Use(func(c *gin.Context) { c.Set(middleware.UserIDKey, "user-1") })
	engine.GET("/events", handler.UserEvents)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest(http.MethodGet, "/events", nil).WithContext(ctx)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, expected := range []string{"id: 1", "event: conversation.title.updated", `"title":"Agent 学习"`, `"sequence":1`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("body %q does not contain %q", body, expected)
		}
	}
}

func TestUserEventsReturnsConflictForReplayGap(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &Handler{events: userevent.New(time.Minute, 10)}
	engine := gin.New()
	engine.Use(func(c *gin.Context) { c.Set(middleware.UserIDKey, "user-1") })
	engine.GET("/events", handler.UserEvents)

	request := httptest.NewRequest(http.MethodGet, "/events?afterSequence=8", nil)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
}
