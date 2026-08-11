package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/example/agent-platform/backend/internal/auth"
	"github.com/example/agent-platform/backend/internal/httpapi/response"
	"github.com/gin-gonic/gin"
)

const UserIDKey = "userID"
const SessionIDKey = "sessionID"

type SessionValidator interface {
	SessionActive(ctx context.Context, userID, sessionID string) (bool, error)
}

func Authenticate(jwtManager *auth.JWTManager, validator SessionValidator) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			response.WriteErrorResp(c, http.StatusUnauthorized, "需要登录")
			c.Abort()
			return
		}
		claims, err := jwtManager.Parse(strings.TrimPrefix(header, "Bearer "))
		if err != nil || claims.Subject == "" || claims.SessionID == "" {
			response.WriteErrorResp(c, http.StatusUnauthorized, "登录状态已失效")
			c.Abort()
			return
		}
		if validator != nil {
			active, validationErr := validator.SessionActive(c.Request.Context(), claims.Subject, claims.SessionID)
			if validationErr != nil || !active {
				response.WriteErrorResp(c, http.StatusUnauthorized, "登录状态已失效")
				c.Abort()
				return
			}
		}
		c.Set(UserIDKey, claims.Subject)
		c.Set(SessionIDKey, claims.SessionID)
		c.Next()
	}
}
