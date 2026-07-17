package middleware

import (
	"net/http"
	"strings"

	"github.com/example/agent-platform/backend/internal/auth"
	"github.com/example/agent-platform/backend/internal/httpapi/response"
	"github.com/gin-gonic/gin"
)

const UserIDKey = "userID"

func Authenticate(jwtManager *auth.JWTManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			response.WriteErrorResp(c, http.StatusUnauthorized, "需要登录")
			c.Abort()
			return
		}
		claims, err := jwtManager.Parse(strings.TrimPrefix(header, "Bearer "))
		if err != nil || claims.Subject == "" {
			response.WriteErrorResp(c, http.StatusUnauthorized, "登录状态已失效")
			c.Abort()
			return
		}
		c.Set(UserIDKey, claims.Subject)
		c.Next()
	}
}
