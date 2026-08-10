package handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/example/agent-platform/backend/internal/httpapi/response"
	"github.com/example/agent-platform/backend/internal/logger"
	"github.com/example/agent-platform/backend/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *Handler) ListConversations(c *gin.Context) {
	limit, err := parsePageLimit(c.Query("limit"))
	if err != nil {
		response.WriteErrorResp(c, http.StatusBadRequest, "分页参数不正确")
		return
	}
	page, err := h.conversations.ListPage(c.Request.Context(), currentUserID(c), c.Query("cursor"), c.Query("q"), limit)
	if errors.Is(err, service.ErrInvalidCursor) || errors.Is(err, service.ErrInvalidPageLimit) {
		response.WriteErrorResp(c, http.StatusBadRequest, "分页参数不正确")
		return
	}
	if err != nil {
		logger.Error(c.Request.Context(), "list conversations failed", logger.Err(err))
		response.WriteErrorResp(c, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	response.WriteOKResp(c, gin.H{"conversations": page.Items, "nextCursor": page.NextCursor, "hasMore": page.HasMore})
}

func (h *Handler) CreateConversation(c *gin.Context) {
	var input struct {
		Title string `json:"title"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		response.WriteErrorResp(c, http.StatusBadRequest, "请求格式不正确")
		return
	}
	conversation, err := h.conversations.Create(c.Request.Context(), currentUserID(c), input.Title)
	if err != nil {
		logger.Error(c.Request.Context(), "create conversation failed", logger.Err(err))
		response.WriteErrorResp(c, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	response.WriteCreatedResp(c, gin.H{"conversation": conversation})
}

func (h *Handler) ListMessages(c *gin.Context) {
	limit, err := parsePageLimit(c.Query("limit"))
	if err != nil {
		response.WriteErrorResp(c, http.StatusBadRequest, "分页参数不正确")
		return
	}
	page, err := h.conversations.ListMessagesPage(c.Request.Context(), currentUserID(c), c.Param("conversationID"), c.Query("cursor"), limit)
	if errors.Is(err, service.ErrInvalidCursor) || errors.Is(err, service.ErrInvalidPageLimit) {
		response.WriteErrorResp(c, http.StatusBadRequest, "分页参数不正确")
		return
	}
	if err != nil {
		logger.Error(c.Request.Context(), "list messages failed", logger.Err(err))
		response.WriteErrorResp(c, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	response.WriteOKResp(c, gin.H{"messages": page.Items, "nextCursor": page.NextCursor, "hasMore": page.HasMore})
}

func parsePageLimit(raw string) (int, error) {
	if raw == "" {
		return 0, nil
	}
	return strconv.Atoi(raw)
}

func (h *Handler) ActivateBranch(c *gin.Context) {
	message, err := h.conversations.ActivateBranch(c.Request.Context(), currentUserID(c), c.Param("conversationID"), c.Param("messageID"))
	switch {
	case errors.Is(err, service.ErrMessageNotFound):
		response.WriteErrorResp(c, http.StatusNotFound, "消息不存在")
	case err != nil:
		logger.Error(c.Request.Context(), "activate conversation branch failed", logger.Err(err))
		response.WriteErrorResp(c, http.StatusInternalServerError, "服务器内部错误")
	default:
		response.WriteOKResp(c, gin.H{"message": message})
	}
}

func (h *Handler) UpdateConversation(c *gin.Context) {
	var input struct {
		Title string `json:"title"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		response.WriteErrorResp(c, http.StatusBadRequest, "请求格式不正确")
		return
	}
	conversation, err := h.conversations.UpdateTitle(c.Request.Context(), currentUserID(c), c.Param("conversationID"), input.Title)
	switch {
	case errors.Is(err, service.ErrEmptyConversationTitle):
		response.WriteErrorResp(c, http.StatusBadRequest, "标题不能为空")
	case errors.Is(err, service.ErrConversationNotFound):
		response.WriteErrorResp(c, http.StatusNotFound, "对话不存在")
	case err != nil:
		logger.Error(c.Request.Context(), "update conversation failed", logger.Err(err))
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
		logger.Error(c.Request.Context(), "delete conversation failed", logger.Err(err))
		response.WriteErrorResp(c, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	c.Status(http.StatusNoContent)
}
