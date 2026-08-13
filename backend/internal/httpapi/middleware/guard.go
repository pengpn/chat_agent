package middleware

import (
	"net/http"
	"strconv"
	"time"

	"github.com/example/agent-platform/backend/internal/alerting"
	"github.com/example/agent-platform/backend/internal/httpapi/response"
	"github.com/example/agent-platform/backend/internal/logger"
	"github.com/example/agent-platform/backend/internal/ratelimit"
	"github.com/gin-gonic/gin"
)

// MaxBodyBytes 限制单个请求体大小，防止超长输入占用内存和带宽。
// 超限后读取失败，由 handler 以 413 响应。
func MaxBodyBytes(limit int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if limit > 0 {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
		}
		c.Next()
	}
}

// UserRateLimit 按登录用户做固定窗口限流。它和额度（Quota）的分工：
// 限流只关心单位时间内的请求次数，防瞬时洪峰和刷接口；
// 额度关心累计 Token 用量，控制成本。limit <= 0 时不限流。
func UserRateLimit(limiter ratelimit.Limiter, alerter alerting.Alerter, action string, limit int, window time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		if limit <= 0 || limiter == nil {
			c.Next()
			return
		}
		userID := c.GetString(UserIDKey)
		decision, err := limiter.Allow(c.Request.Context(), action+":"+userID, limit, window)
		if err != nil {
			// 限流组件故障时放行，避免 Redis 抖动阻断全部对话；
			// 成本和并发仍有额度与并发限制兜底。
			logger.Error(c.Request.Context(), "user rate limiter failed open", logger.Err(err), logger.String("action", action))
			c.Next()
			return
		}
		if !decision.Allowed {
			seconds := int(decision.RetryAfter.Seconds())
			if seconds < 1 {
				seconds = 1
			}
			c.Header("Retry-After", strconv.Itoa(seconds))
			if alerter != nil {
				alerter.Alert(c.Request.Context(), alerting.KindCallSurge,
					logger.String("user_id", userID),
					logger.String("action", action),
					logger.Int("limit", limit),
				)
			}
			response.WriteErrorResp(c, http.StatusTooManyRequests, "请求过于频繁，请稍后再试")
			c.Abort()
			return
		}
		c.Next()
	}
}
