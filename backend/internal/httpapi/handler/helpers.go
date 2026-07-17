package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/example/agent-platform/backend/internal/httpapi/middleware"
	"github.com/example/agent-platform/backend/internal/httpapi/response"
	"github.com/gin-gonic/gin"
)

func currentUserID(c *gin.Context) string { return c.GetString(middleware.UserIDKey) }

func decodeJSON(c *gin.Context, output any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		response.WriteErrorResp(c, http.StatusBadRequest, "请求格式不正确")
		return false
	}
	return true
}

func safeError(err error) string {
	value := strings.TrimSpace(err.Error())
	if len(value) > 400 {
		return value[:400]
	}
	return value
}
