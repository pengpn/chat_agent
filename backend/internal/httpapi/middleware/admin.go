package middleware

import (
	"context"
	"net/http"

	"github.com/example/agent-platform/backend/internal/httpapi/response"
	"github.com/gin-gonic/gin"
)

// AdminChecker 判断当前登录用户是否具有管理员权限。
type AdminChecker interface {
	IsAdmin(ctx context.Context, userID string) (bool, error)
}

// RequireAdmin 必须挂在 Authenticate 之后使用。
// 查询失败按非管理员处理，避免鉴权故障时放行特权操作。
func RequireAdmin(checker AdminChecker) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.GetString(UserIDKey)
		admin, err := checker.IsAdmin(c.Request.Context(), userID)
		if err != nil || !admin {
			response.WriteErrorResp(c, http.StatusForbidden, "需要管理员权限")
			c.Abort()
			return
		}
		c.Next()
	}
}
