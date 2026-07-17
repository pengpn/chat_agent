package handler

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/example/agent-platform/backend/internal/httpapi/response"
	"github.com/example/agent-platform/backend/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *Handler) Register(c *gin.Context) {
	var input struct{ Email, Password, DisplayName string }
	if !decodeJSON(c, &input) {
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
		slog.Error("register", "error", err)
		response.WriteErrorResp(c, http.StatusInternalServerError, "无法创建账户")
	default:
		response.WriteOKResp(c, gin.H{"user": result.User, "accessToken": result.AccessToken, "expiresAt": result.ExpiresAt})
	}
}

func (h *Handler) Login(c *gin.Context) {
	var input struct{ Email, Password string }
	if !decodeJSON(c, &input) {
		return
	}
	result, err := h.auth.Login(c.Request.Context(), service.LoginInput{Email: input.Email, Password: input.Password})
	if errors.Is(err, service.ErrInvalidCredentials) {
		response.WriteErrorResp(c, http.StatusUnauthorized, "邮箱或密码错误")
		return
	}
	if err != nil {
		slog.Error("login", "error", err)
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
		slog.Error("get current user", "error", err)
		response.WriteErrorResp(c, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	response.WriteOKResp(c, gin.H{"user": user})
}
