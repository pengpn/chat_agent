package middleware

import (
	"log/slog"
	"net/http"

	"github.com/example/agent-platform/backend/internal/httpapi/response"
	"github.com/gin-gonic/gin"
)

func Recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.Error("panic", "error", recovered)
				if !c.Writer.Written() {
					response.WriteErrorResp(c, http.StatusInternalServerError, "服务器发生异常")
				}
				c.Abort()
			}
		}()
		c.Next()
	}
}
