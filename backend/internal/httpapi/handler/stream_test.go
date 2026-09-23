package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/example/agent-platform/backend/internal/httpapi/middleware"
	"github.com/example/agent-platform/backend/internal/streamcache"
	"github.com/gin-gonic/gin"
)

func TestResumeMessageStreamReplaysOnlyEventsAfterSequence(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cache := streamcache.New(time.Minute, 10)
	cache.Start("message-1", "user-1")
	cache.Append("message-1", "delta", map[string]any{"content": "A"}, false)
	cache.Append("message-1", "citations", map[string]any{"citations": []map[string]any{{"ordinal": 1, "sourceName": "规则.pdf"}}}, false)
	cache.Append("message-1", "done", map[string]any{"status": "complete"}, true)
	handler := &Handler{streams: cache}
	router := gin.New()
	router.GET("/messages/:messageID/stream", func(c *gin.Context) {
		c.Set(middleware.UserIDKey, "user-1")
		handler.ResumeMessageStream(c)
	})

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/messages/message-1/stream?afterSequence=1", nil)
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if strings.Contains(body, "content\":\"A") || !strings.Contains(body, "id: 2\nevent: citations") || !strings.Contains(body, "id: 3\nevent: done") {
		t.Fatalf("body=%q", body)
	}
}
