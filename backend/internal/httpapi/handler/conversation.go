package handler

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/example/agent-platform/backend/internal/httpapi/response"
	"github.com/example/agent-platform/backend/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *Handler) ListConversations(c *gin.Context) {
	items, err := h.conversations.List(c.Request.Context(), currentUserID(c))
	if err != nil {
		slog.Error("list conversations", "error", err)
		response.WriteErrorResp(c, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	response.WriteOKResp(c, gin.H{"conversations": items})
}

func (h *Handler) CreateConversation(c *gin.Context) {
	var input struct {
		Title string `json:"title"`
	}
	if !decodeJSON(c, &input) {
		return
	}
	conversation, err := h.conversations.Create(c.Request.Context(), currentUserID(c), input.Title)
	if err != nil {
		slog.Error("create conversation", "error", err)
		response.WriteErrorResp(c, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	response.WriteCreatedResp(c, gin.H{"conversation": conversation})
}

func (h *Handler) ListMessages(c *gin.Context) {
	items, err := h.conversations.ListMessages(c.Request.Context(), currentUserID(c), c.Param("conversationID"))
	if err != nil {
		slog.Error("list messages", "error", err)
		response.WriteErrorResp(c, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	response.WriteOKResp(c, gin.H{"messages": items})
}

func (h *Handler) UpdateConversation(c *gin.Context) {
	var input struct {
		Title string `json:"title"`
	}
	if !decodeJSON(c, &input) {
		return
	}
	conversation, err := h.conversations.UpdateTitle(c.Request.Context(), currentUserID(c), c.Param("conversationID"), input.Title)
	switch {
	case errors.Is(err, service.ErrEmptyConversationTitle):
		response.WriteErrorResp(c, http.StatusBadRequest, "标题不能为空")
	case errors.Is(err, service.ErrConversationNotFound):
		response.WriteErrorResp(c, http.StatusNotFound, "对话不存在")
	case err != nil:
		slog.Error("update conversation", "error", err)
		response.WriteErrorResp(c, http.StatusInternalServerError, "服务器内部错误")
	default:
		response.WriteOKResp(c, gin.H{"conversation": conversation})
	}
}

func (h *Handler) DeleteConversation(c *gin.Context) {
	err := h.conversations.Delete(c.Request.Context(), currentUserID(c), c.Param("conversationID"))
	if errors.Is(err, service.ErrConversationNotFound) {
		response.WriteErrorResp(c, http.StatusNotFound, "对话不存在")
		return
	}
	if err != nil {
		slog.Error("delete conversation", "error", err)
		response.WriteErrorResp(c, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	c.Status(http.StatusNoContent)
}
