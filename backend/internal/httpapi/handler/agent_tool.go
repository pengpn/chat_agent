package handler

import (
	"errors"
	"net/http"

	"github.com/example/agent-platform/backend/internal/domain"
	"github.com/example/agent-platform/backend/internal/httpapi/response"
	"github.com/example/agent-platform/backend/internal/logger"
	"github.com/example/agent-platform/backend/internal/repository"
	"github.com/example/agent-platform/backend/internal/service"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func (h *Handler) AgentToolState(c *gin.Context) {
	state, err := h.toolState.Get(c.Request.Context(), currentUserID(c), c.Param("conversationID"))
	if err != nil {
		writeAgentToolError(c, err)
		return
	}
	response.WriteOKResp(c, state)
}
func (h *Handler) RespondToolInteraction(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 12<<10)
	var input service.ToolUserResponse
	if err := c.ShouldBindJSON(&input); err != nil {
		response.WriteErrorResp(c, http.StatusBadRequest, "答复格式不正确")
		return
	}
	if err := h.interactions.Respond(c.Request.Context(), currentUserID(c), c.Param("conversationID"), c.Param("interactionID"), input); err != nil {
		writeAgentToolError(c, err)
		return
	}
	response.WriteOKResp(c, gin.H{"responded": true})
}
func (h *Handler) ReadAgentArtifact(c *gin.Context) {
	item, err := h.artifacts.Get(c.Request.Context(), currentUserID(c), c.Param("conversationID"), c.Param("artifactID"))
	if err != nil {
		writeAgentToolError(c, err)
		return
	}
	// 以 JSON 返回不可信文本，避免 HTML 产物被浏览器当同源页面执行。
	response.WriteOKResp(c, item)
}
func writeAgentToolError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, domain.ErrNotFound), errors.Is(err, gorm.ErrRecordNotFound):
		response.WriteErrorResp(c, http.StatusNotFound, "会话或工具记录不存在")
	case errors.Is(err, repository.ErrInteractionClosed):
		response.WriteErrorResp(c, http.StatusConflict, err.Error())
	case errors.Is(err, service.ErrInvalidToolInput):
		response.WriteErrorResp(c, http.StatusBadRequest, err.Error())
	default:
		logger.Error(c.Request.Context(), "agent tool API failed", logger.Err(err))
		response.WriteErrorResp(c, http.StatusInternalServerError, "工具状态处理失败，请重试")
	}
}
