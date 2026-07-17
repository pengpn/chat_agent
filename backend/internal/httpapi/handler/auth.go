package handler

import (
	"errors"
	"net/http"

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
		Email:       input.Email,
		Password:    input.Password,
		DisplayName: input.DisplayName,
	})
	switch {
	case errors.Is(err, service.ErrInvalidEmail):
		response.WriteErrorResp(c, http.StatusBadRequest, "请输入有效的邮箱地址")
	case errors.Is(err, service.ErrPasswordTooShort):
		response.WriteErrorResp(c, http.StatusBadRequest, "密码至少需要 8 个字符")
	case errors.Is(err, service.ErrDisplayNameTooLong):
		response.WriteErrorResp(c, http.StatusBadRequest, "昵称不能超过 80 个字符")
	case errors.Is(err, service.ErrEmailAlreadyRegistered):
		response.WriteErrorResp(c, http.StatusConflict, "该邮箱已注册")
	case err != nil:
		logger.Error(c.Request.Context(), "register failed", logger.Err(err))
		response.WriteErrorResp(c, http.StatusInternalServerError, "无法创建账户")
	default:
		response.WriteOKResp(c, gin.H{"user": result.User, "accessToken": result.AccessToken, "expiresAt": result.ExpiresAt})
	}
}

func (h *Handler) Login(c *gin.Context) {
	var input struct{ Email, Password string }
	if err := c.ShouldBindJSON(&input); err != nil {
		response.WriteErrorResp(c, http.StatusBadRequest, "请求格式不正确")
		return
	}
	result, err := h.auth.Login(c.Request.Context(), service.LoginInput{Email: input.Email, Password: input.Password})
	if errors.Is(err, service.ErrInvalidCredentials) {
		response.WriteErrorResp(c, http.StatusUnauthorized, "邮箱或密码错误")
		return
	}
	if err != nil {
		logger.Error(c.Request.Context(), "login failed", logger.Err(err))
		response.WriteErrorResp(c, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	response.WriteOKResp(c, gin.H{"user": result.User, "accessToken": result.AccessToken, "expiresAt": result.ExpiresAt})
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
