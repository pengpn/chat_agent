package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/example/agent-platform/backend/internal/alerting"
	"github.com/example/agent-platform/backend/internal/logger"
	"github.com/example/agent-platform/backend/internal/ratelimit"
	"github.com/gin-gonic/gin"
)

func init() { gin.SetMode(gin.TestMode) }

type recordingLimiter struct {
	allowed  bool
	lastKey  string
	failOpen bool
}

func (l *recordingLimiter) Allow(_ context.Context, key string, limit int, window time.Duration) (ratelimit.Decision, error) {
	l.lastKey = key
	if l.failOpen {
		return ratelimit.Decision{}, errors.New("redis down")
	}
	return ratelimit.Decision{Allowed: l.allowed, RetryAfter: 2 * time.Second}, nil
}

type recordingAlerter struct {
	kinds []alerting.Kind
}

func (a *recordingAlerter) Alert(_ context.Context, kind alerting.Kind, _ ...logger.Field) {
	a.kinds = append(a.kinds, kind)
}

func newUserRateLimitRouter(limiter ratelimit.Limiter, alerter alerting.Alerter) http.Handler {
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set(UserIDKey, "user-1")
		c.Next()
	})
	engine.Use(UserRateLimit(limiter, alerter, "chat", 2, time.Minute))
	engine.POST("/chat", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	return engine
}

func TestUserRateLimitRejectsOverLimit(t *testing.T) {
	limiter := &recordingLimiter{allowed: false}
	alerter := &recordingAlerter{}
	router := newUserRateLimitRouter(limiter, alerter)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/chat", strings.NewReader("{}"))
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d", recorder.Code)
	}
	if recorder.Header().Get("Retry-After") == "" {
		t.Fatal("missing Retry-After header")
	}
	if limiter.lastKey != "chat:user-1" {
		t.Fatalf("key = %q", limiter.lastKey)
	}
	if len(alerter.kinds) != 1 || alerter.kinds[0] != alerting.KindCallSurge {
		t.Fatalf("alerts = %#v", alerter.kinds)
	}
}

func TestUserRateLimitFailsOpenOnLimiterError(t *testing.T) {
	router := newUserRateLimitRouter(&recordingLimiter{failOpen: true}, &recordingAlerter{})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/chat", strings.NewReader("{}"))
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, limiter outage must not block requests", recorder.Code)
	}
}

func TestUserRateLimitDisabledPassesThrough(t *testing.T) {
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set(UserIDKey, "user-1")
		c.Next()
	})
	engine.Use(UserRateLimit(&recordingLimiter{allowed: false}, nil, "chat", 0, time.Minute))
	engine.POST("/chat", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/chat", strings.NewReader("{}"))
	engine.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
}

func TestMaxBodyBytesRejectsOversizedBody(t *testing.T) {
	engine := gin.New()
	engine.POST("/chat", MaxBodyBytes(8), func(c *gin.Context) {
		var payload map[string]any
		if err := c.ShouldBindJSON(&payload); err != nil {
			var maxBytesError *http.MaxBytesError
			if errors.As(err, &maxBytesError) {
				c.String(http.StatusRequestEntityTooLarge, "too large")
				return
			}
			c.String(http.StatusBadRequest, "bad request")
			return
		}
		c.String(http.StatusOK, "ok")
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/chat", strings.NewReader(`{"content":"超过限制的请求体内容"}`))
	engine.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, body = %q", recorder.Code, recorder.Body.String())
	}
}

type staticAdminChecker struct{ admin bool }

func (s staticAdminChecker) IsAdmin(context.Context, string) (bool, error) { return s.admin, nil }

func TestRequireAdminAllowsAdminOnly(t *testing.T) {
	build := func(admin bool) http.Handler {
		engine := gin.New()
		engine.Use(func(c *gin.Context) {
			c.Set(UserIDKey, "user-1")
			c.Next()
		})
		engine.Use(RequireAdmin(staticAdminChecker{admin: admin}))
		engine.GET("/admin", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
		return engine
	}
	recorder := httptest.NewRecorder()
	build(false).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin", nil))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("non-admin status = %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	build(true).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("admin status = %d", recorder.Code)
	}
}
