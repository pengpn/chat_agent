package handler

import (
	"strings"

	"github.com/example/agent-platform/backend/internal/httpapi/middleware"
	"github.com/gin-gonic/gin"
)

func currentUserID(c *gin.Context) string { return c.GetString(middleware.UserIDKey) }

func safeError(err error) string {
	value := strings.TrimSpace(err.Error())
	if len(value) > 400 {
		return value[:400]
	}
	return value
}
