package handler

import (
	"errors"
	"net/http"

	"github.com/example/agent-platform/backend/internal/domain"
	"github.com/example/agent-platform/backend/internal/httpapi/response"
	"github.com/example/agent-platform/backend/internal/logger"
	"github.com/example/agent-platform/backend/internal/service"
	"github.com/gin-gonic/gin"
)

// GetMyQuota 返回当前用户的额度快照，供前端展示剩余用量。
func (h *Handler) GetMyQuota(c *gin.Context) {
	status, err := h.quotas.Status(c.Request.Context(), currentUserID(c))
	if err != nil {
		logger.Error(c.Request.Context(), "get my quota failed", logger.Err(err))
		response.WriteErrorResp(c, http.StatusInternalServerError, "无法加载额度信息")
		return
	}
	response.WriteOKResp(c, gin.H{"quota": status})
}

func (h *Handler) BanUser(c *gin.Context) {
	var input struct {
		Reason string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		response.WriteErrorResp(c, http.StatusBadRequest, "请求格式不正确")
		return
	}
	targetID := c.Param("userID")
	err := h.admin.Ban(c.Request.Context(), currentUserID(c), targetID, input.Reason, c.Request.UserAgent())
	if h.writeAdminError(c, err, "ban user failed") {
		return
	}
	response.WriteOKResp(c, gin.H{"banned": true})
}

func (h *Handler) UnbanUser(c *gin.Context) {
	targetID := c.Param("userID")
	err := h.admin.Unban(c.Request.Context(), currentUserID(c), targetID, c.Request.UserAgent())
	if h.writeAdminError(c, err, "unban user failed") {
		return
	}
	response.WriteOKResp(c, gin.H{"banned": false})
}

// AdjustUserQuota 调整目标用户额度。字段缺省时按 0（沿用默认）处理；
// 传入负数表示该用户不限量。
func (h *Handler) AdjustUserQuota(c *gin.Context) {
	var input struct {
		DailyTokenLimit   int64 `json:"dailyTokenLimit"`
		MonthlyTokenLimit int64 `json:"monthlyTokenLimit"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		response.WriteErrorResp(c, http.StatusBadRequest, "请求格式不正确")
		return
	}
	targetID := c.Param("userID")
	status, err := h.admin.SetQuotaLimits(c.Request.Context(), currentUserID(c), targetID, input.DailyTokenLimit, input.MonthlyTokenLimit, c.Request.UserAgent())
	if h.writeAdminError(c, err, "adjust user quota failed") {
		return
	}
	response.WriteOKResp(c, gin.H{"quota": status})
}

func (h *Handler) GetUserQuota(c *gin.Context) {
	status, err := h.admin.QuotaStatus(c.Request.Context(), c.Param("userID"))
	if err != nil {
		logger.Error(c.Request.Context(), "get user quota failed", logger.Err(err))
		response.WriteErrorResp(c, http.StatusInternalServerError, "无法加载额度信息")
		return
	}
	response.WriteOKResp(c, gin.H{"quota": status})
}

func (h *Handler) writeAdminError(c *gin.Context, err error, logMessage string) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, domain.ErrNotFound):
		response.WriteErrorResp(c, http.StatusNotFound, "用户不存在")
	case errors.Is(err, service.ErrAdminSelfOperation):
		response.WriteErrorResp(c, http.StatusBadRequest, "不能对自己执行该操作")
	case errors.Is(err, service.ErrInvalidQuotaLimit):
		response.WriteErrorResp(c, http.StatusBadRequest, "额度取值无效：0 表示默认，-1 表示不限量，其余需为正数")
	default:
		logger.Error(c.Request.Context(), logMessage, logger.Err(err))
		response.WriteErrorResp(c, http.StatusInternalServerError, "服务器内部错误")
	}
	return true
}
