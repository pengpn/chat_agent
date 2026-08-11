package handler

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/example/agent-platform/backend/internal/httpapi/response"
	"github.com/example/agent-platform/backend/internal/logger"
	"github.com/example/agent-platform/backend/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *Handler) Register(c *gin.Context) {
	var input struct{ Email, Password, DisplayName string }
	if err := c.ShouldBindJSON(&input); err != nil {
		response.WriteErrorResp(c, http.StatusBadRequest, "请求格式不正确")
		return
	}
	result, err := h.auth.Register(c.Request.Context(), service.RegisterInput{
		Email: input.Email, Password: input.Password, DisplayName: input.DisplayName, Client: clientInfo(c),
	})
	if h.writeAuthError(c, err, "register failed", "无法创建账户") {
		return
	}
	h.writeAuthResult(c, result)
}

func (h *Handler) Login(c *gin.Context) {
	var input struct{ Email, Password string }
	if err := c.ShouldBindJSON(&input); err != nil {
		response.WriteErrorResp(c, http.StatusBadRequest, "请求格式不正确")
		return
	}
	result, err := h.auth.Login(c.Request.Context(), service.LoginInput{Email: input.Email, Password: input.Password, Client: clientInfo(c)})
	if h.writeAuthError(c, err, "login failed", "服务器内部错误") {
		return
	}
	h.writeAuthResult(c, result)
}

func (h *Handler) Refresh(c *gin.Context) {
	raw, _ := c.Cookie(h.cookieName())
	result, err := h.auth.Refresh(c.Request.Context(), raw, clientInfo(c))
	if h.writeAuthError(c, err, "refresh session failed", "无法刷新登录状态") {
		h.clearRefreshCookie(c)
		return
	}
	h.writeAuthResult(c, result)
}

func (h *Handler) Logout(c *gin.Context) {
	raw, _ := c.Cookie(h.cookieName())
	if err := h.auth.Logout(c.Request.Context(), raw, clientInfo(c)); err != nil {
		logger.Error(c.Request.Context(), "logout failed", logger.Err(err))
	}
	h.clearRefreshCookie(c)
	response.WriteOKResp(c, gin.H{"loggedOut": true})
}

func (h *Handler) LogoutAll(c *gin.Context) {
	if err := h.auth.LogoutAll(c.Request.Context(), currentUserID(c), currentSessionID(c), clientInfo(c)); err != nil {
		logger.Error(c.Request.Context(), "logout all sessions failed", logger.Err(err))
		response.WriteErrorResp(c, http.StatusInternalServerError, "无法退出全部设备")
		return
	}
	h.clearRefreshCookie(c)
	response.WriteOKResp(c, gin.H{"loggedOut": true})
}

func (h *Handler) Me(c *gin.Context) {
	user, err := h.auth.Me(c.Request.Context(), currentUserID(c))
	if errors.Is(err, service.ErrUnauthenticated) {
		response.WriteErrorResp(c, http.StatusUnauthorized, "登录状态已失效")
		return
	}
	if err != nil {
		logger.Error(c.Request.Context(), "get current user failed", logger.Err(err))
		response.WriteErrorResp(c, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	response.WriteOKResp(c, gin.H{"user": user})
}

func (h *Handler) ListSessions(c *gin.Context) {
	items, err := h.auth.Sessions(c.Request.Context(), currentUserID(c), currentSessionID(c))
	if err != nil {
		logger.Error(c.Request.Context(), "list account sessions failed", logger.Err(err))
		response.WriteErrorResp(c, http.StatusInternalServerError, "无法加载登录设备")
		return
	}
	response.WriteOKResp(c, gin.H{"sessions": items})
}

func (h *Handler) RevokeSession(c *gin.Context) {
	targetID := c.Param("sessionID")
	if err := h.auth.RevokeSession(c.Request.Context(), currentUserID(c), currentSessionID(c), targetID, clientInfo(c)); err != nil {
		if errors.Is(err, service.ErrSessionNotFound) {
			response.WriteErrorResp(c, http.StatusNotFound, "该设备会话不存在或已退出")
			return
		}
		logger.Error(c.Request.Context(), "revoke account session failed", logger.Err(err))
		response.WriteErrorResp(c, http.StatusInternalServerError, "无法退出该设备")
		return
	}
	if targetID == currentSessionID(c) {
		h.clearRefreshCookie(c)
	}
	response.WriteOKResp(c, gin.H{"revoked": true, "current": targetID == currentSessionID(c)})
}

func (h *Handler) VerifyEmail(c *gin.Context) {
	var input struct{ Token string }
	if err := c.ShouldBindJSON(&input); err != nil {
		response.WriteErrorResp(c, http.StatusBadRequest, "请求格式不正确")
		return
	}
	user, err := h.auth.VerifyEmail(c.Request.Context(), input.Token, clientInfo(c))
	if errors.Is(err, service.ErrInvalidAccountToken) {
		response.WriteErrorResp(c, http.StatusBadRequest, "验证链接无效、已使用或已过期")
		return
	}
	if err != nil {
		logger.Error(c.Request.Context(), "verify email failed", logger.Err(err))
		response.WriteErrorResp(c, http.StatusInternalServerError, "暂时无法验证邮箱")
		return
	}
	response.WriteOKResp(c, gin.H{"user": user})
}

func (h *Handler) ResendVerification(c *gin.Context) {
	err := h.auth.ResendVerification(c.Request.Context(), currentUserID(c), clientInfo(c))
	if h.writeAuthError(c, err, "resend verification failed", "暂时无法发送验证邮件") {
		return
	}
	response.WriteOKResp(c, gin.H{"sent": true})
}

func (h *Handler) ForgotPassword(c *gin.Context) {
	var input struct{ Email string }
	if err := c.ShouldBindJSON(&input); err != nil {
		response.WriteErrorResp(c, http.StatusBadRequest, "请求格式不正确")
		return
	}
	if err := h.auth.ForgotPassword(c.Request.Context(), input.Email, clientInfo(c)); err != nil {
		if h.writeAuthError(c, err, "forgot password failed", "暂时无法处理密码重置") {
			return
		}
	}
	response.WriteOKResp(c, gin.H{"accepted": true})
}

func (h *Handler) ResetPassword(c *gin.Context) {
	var input struct{ Token, NewPassword string }
	if err := c.ShouldBindJSON(&input); err != nil {
		response.WriteErrorResp(c, http.StatusBadRequest, "请求格式不正确")
		return
	}
	err := h.auth.ResetPassword(c.Request.Context(), input.Token, input.NewPassword, clientInfo(c))
	if h.writeAuthError(c, err, "reset password failed", "暂时无法重置密码") {
		return
	}
	h.clearRefreshCookie(c)
	response.WriteOKResp(c, gin.H{"reset": true})
}

func (h *Handler) ChangePassword(c *gin.Context) {
	var input struct{ CurrentPassword, NewPassword string }
	if err := c.ShouldBindJSON(&input); err != nil {
		response.WriteErrorResp(c, http.StatusBadRequest, "请求格式不正确")
		return
	}
	err := h.auth.ChangePassword(c.Request.Context(), currentUserID(c), currentSessionID(c), input.CurrentPassword, input.NewPassword, clientInfo(c))
	if h.writeAuthError(c, err, "change password failed", "暂时无法修改密码") {
		return
	}
	h.clearRefreshCookie(c)
	response.WriteOKResp(c, gin.H{"changed": true, "loggedOut": true})
}

func (h *Handler) ExportAccount(c *gin.Context) {
	data, err := h.auth.ExportAccount(c.Request.Context(), currentUserID(c), currentSessionID(c), clientInfo(c))
	if err != nil {
		logger.Error(c.Request.Context(), "export account failed", logger.Err(err))
		response.WriteErrorResp(c, http.StatusInternalServerError, "暂时无法导出个人数据")
		return
	}
	content, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		logger.Error(c.Request.Context(), "encode account export failed", logger.Err(err))
		response.WriteErrorResp(c, http.StatusInternalServerError, "暂时无法导出个人数据")
		return
	}
	c.Header("Content-Disposition", "attachment; filename=agent-workspace-export.json")
	c.Data(http.StatusOK, "application/json; charset=utf-8", content)
}

func (h *Handler) DeleteAccount(c *gin.Context) {
	var input struct{ Password, Confirmation string }
	if err := c.ShouldBindJSON(&input); err != nil {
		response.WriteErrorResp(c, http.StatusBadRequest, "请求格式不正确")
		return
	}
	err := h.auth.DeleteAccount(c.Request.Context(), currentUserID(c), currentSessionID(c), input.Password, input.Confirmation, clientInfo(c))
	if h.writeAuthError(c, err, "delete account failed", "暂时无法注销账户") {
		return
	}
	h.clearRefreshCookie(c)
	response.WriteOKResp(c, gin.H{"deleted": true})
}

func (h *Handler) writeAuthResult(c *gin.Context, result service.AuthResult) {
	h.setRefreshCookie(c, result.RefreshToken, result.RefreshExpiresAt)
	response.WriteOKResp(c, gin.H{"user": result.User, "accessToken": result.AccessToken, "expiresAt": result.ExpiresAt})
}

func (h *Handler) writeAuthError(c *gin.Context, err error, logMessage, fallback string) bool {
	if err == nil {
		return false
	}
	var rateErr *service.RateLimitError
	switch {
	case errors.As(err, &rateErr):
		seconds := int(math.Ceil(rateErr.RetryAfter.Seconds()))
		if seconds < 1 {
			seconds = 1
		}
		c.Header("Retry-After", strconv.Itoa(seconds))
		response.WriteErrorResp(c, http.StatusTooManyRequests, "操作过于频繁，请稍后再试")
	case errors.Is(err, service.ErrInvalidEmail):
		response.WriteErrorResp(c, http.StatusBadRequest, "请输入有效的邮箱地址")
	case errors.Is(err, service.ErrPasswordTooShort):
		response.WriteErrorResp(c, http.StatusBadRequest, "密码至少需要 8 个字符")
	case errors.Is(err, service.ErrPasswordTooLong):
		response.WriteErrorResp(c, http.StatusBadRequest, "密码不能超过 72 个字节")
	case errors.Is(err, service.ErrPasswordUnchanged):
		response.WriteErrorResp(c, http.StatusBadRequest, "新密码不能与当前密码相同")
	case errors.Is(err, service.ErrDisplayNameTooLong):
		response.WriteErrorResp(c, http.StatusBadRequest, "昵称不能超过 80 个字符")
	case errors.Is(err, service.ErrEmailAlreadyRegistered):
		response.WriteErrorResp(c, http.StatusConflict, "该邮箱已注册")
	case errors.Is(err, service.ErrInvalidCredentials):
		response.WriteErrorResp(c, http.StatusUnauthorized, "邮箱或密码错误")
	case errors.Is(err, service.ErrInvalidRefreshToken):
		response.WriteErrorResp(c, http.StatusUnauthorized, "登录状态已失效")
	case errors.Is(err, service.ErrInvalidAccountToken):
		response.WriteErrorResp(c, http.StatusBadRequest, "链接无效、已使用或已过期")
	case errors.Is(err, service.ErrEmailDeliveryUnavailable):
		response.WriteErrorResp(c, http.StatusServiceUnavailable, "邮件服务暂时不可用，请稍后重试")
	case errors.Is(err, service.ErrAccountDeleteConfirmation):
		response.WriteErrorResp(c, http.StatusBadRequest, "请输入 DELETE 确认注销账户")
	default:
		logger.Error(c.Request.Context(), logMessage, logger.Err(err))
		response.WriteErrorResp(c, http.StatusInternalServerError, fallback)
	}
	return true
}

func clientInfo(c *gin.Context) service.ClientInfo {
	return service.ClientInfo{IP: c.ClientIP(), UserAgent: c.Request.UserAgent(), DeviceName: c.GetHeader("X-Device-Name")}
}

func (h *Handler) setRefreshCookie(c *gin.Context, value string, expiresAt time.Time) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name: h.cookieName(), Value: value, Path: "/api/v1/auth", Expires: expiresAt,
		MaxAge: int(time.Until(expiresAt).Seconds()), HttpOnly: true, Secure: h.refreshCookie.Secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (h *Handler) clearRefreshCookie(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name: h.cookieName(), Value: "", Path: "/api/v1/auth", Expires: time.Unix(1, 0),
		MaxAge: -1, HttpOnly: true, Secure: h.refreshCookie.Secure, SameSite: http.SameSiteLaxMode,
	})
}

func (h *Handler) cookieName() string {
	if h.refreshCookie.Name == "" {
		return "agent_refresh"
	}
	return h.refreshCookie.Name
}
