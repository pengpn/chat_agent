package middleware

import (
	"time"

	"github.com/example/agent-platform/backend/internal/logger"
	"github.com/gin-gonic/gin"
)

func RequestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		started := time.Now()
		c.Next()

		path := c.FullPath()
		if path == "" {
			path = c.Request.URL.Path
		}
		attributes := []logger.Field{
			logger.String("method", c.Request.Method),
			logger.String("path", path),
			logger.Int("status", c.Writer.Status()),
			logger.Int64("latency_ms", time.Since(started).Milliseconds()),
			logger.Int("response_bytes", c.Writer.Size()),
			logger.String("client_ip", c.ClientIP()),
		}
		if userID := c.GetString(UserIDKey); userID != "" {
			attributes = append(attributes, logger.String("user_id", userID))
		}
		logger.Info(c.Request.Context(), "http request completed", attributes...)
	}
}
