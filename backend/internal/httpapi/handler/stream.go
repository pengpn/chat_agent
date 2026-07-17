package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/example/agent-platform/backend/internal/httpapi/response"
	"github.com/example/agent-platform/backend/internal/logger"
	"github.com/example/agent-platform/backend/internal/provider"
	"github.com/example/agent-platform/backend/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *Handler) StreamMessage(c *gin.Context) {
	var input struct {
		Content     string   `json:"content"`
		Provider    string   `json:"provider"`
		Model       string   `json:"model"`
		Temperature *float64 `json:"temperature"`
	}
	if !decodeJSON(c, &input) {
		return
	}
	session, err := h.chat.Prepare(c.Request.Context(), currentUserID(c), c.Param("conversationID"), service.ChatInput{
		Content:     input.Content,
		Provider:    input.Provider,
		Model:       input.Model,
		Temperature: input.Temperature,
	})
	var providerSelectionError *service.ProviderSelectionError
	switch {
	case errors.Is(err, service.ErrInvalidMessageLength):
		response.WriteErrorResp(c, http.StatusBadRequest, "消息长度应在 1 到 100000 个字符之间")
		return
	case errors.Is(err, service.ErrInvalidTemperature):
		response.WriteErrorResp(c, http.StatusBadRequest, "temperature 必须在 0 到 2 之间")
		return
	case errors.Is(err, service.ErrConversationNotFound):
		response.WriteErrorResp(c, http.StatusNotFound, "对话不存在")
		return
	case errors.As(err, &providerSelectionError):
		response.WriteErrorResp(c, http.StatusBadRequest, providerSelectionError.Error())
		return
	case err != nil:
		logger.Error(c.Request.Context(), "prepare message stream failed", logger.Err(err))
		response.WriteErrorResp(c, http.StatusInternalServerError, "服务器内部错误")
		return
	}

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		response.WriteErrorResp(c, http.StatusInternalServerError, "服务器不支持流式响应")
		return
	}
	c.Header("Content-Type", "text/event-stream; charset=utf-8")
	c.Header("Cache-Control", "no-cache, no-transform")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	writeSSE(c.Writer, flusher, "metadata", gin.H{"userMessage": session.UserMessage, "assistantMessage": session.AssistantMessage})

	completed, status, err := h.chat.Stream(c.Request.Context(), session, func(event provider.StreamEvent) {
		if event.Err != nil {
			writeSSE(c.Writer, flusher, "error", gin.H{"message": "模型调用失败：" + safeError(event.Err)})
			return
		}
		if event.Delta != "" {
			writeSSE(c.Writer, flusher, "delta", gin.H{"content": event.Delta})
		}
	})
	if err != nil {
		logger.Error(c.Request.Context(), "finalize streamed message failed", logger.Err(err))
		return
	}
	logger.Info(c.Request.Context(), "model stream completed",
		logger.String("conversation_id", c.Param("conversationID")),
		logger.String("assistant_message_id", completed.ID),
		logger.String("provider", completed.Provider),
		logger.String("model", completed.Model),
		logger.String("status", status),
		logger.Int64("input_tokens", completed.InputTokens),
		logger.Int64("output_tokens", completed.OutputTokens),
		logger.Int64("total_tokens", completed.TotalTokens),
		logger.Bool("usage_reported", completed.UsageReported),
		logger.Int64("cost_nano_usd", completed.CostNanoUSD),
		logger.Bool("cost_known", completed.CostKnown),
	)
	if status == "complete" {
		writeSSE(c.Writer, flusher, "done", gin.H{"assistantMessage": completed})
	}
}

func writeSSE(w http.ResponseWriter, flusher http.Flusher, name string, payload any) {
	data, _ := json.Marshal(payload)
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, data)
	flusher.Flush()
}
