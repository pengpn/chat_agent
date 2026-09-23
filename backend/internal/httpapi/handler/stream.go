package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/example/agent-platform/backend/internal/database/model"
	"github.com/example/agent-platform/backend/internal/httpapi/response"
	"github.com/example/agent-platform/backend/internal/llmgateway"
	"github.com/example/agent-platform/backend/internal/logger"
	"github.com/example/agent-platform/backend/internal/provider"
	"github.com/example/agent-platform/backend/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *Handler) StreamMessage(c *gin.Context) {
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		response.WriteErrorResp(c, http.StatusInternalServerError, "服务器不支持流式响应")
		return
	}
	session, ok := h.prepareChatSession(c)
	if !ok {
		return
	}
	h.streamPreparedSession(c, flusher, session)
}

func (h *Handler) RegenerateMessage(c *gin.Context) {
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		response.WriteErrorResp(c, http.StatusInternalServerError, "服务器不支持流式响应")
		return
	}
	var input struct {
		Provider     string   `json:"provider"`
		Model        string   `json:"model"`
		Temperature  *float64 `json:"temperature"`
		OutputFormat string   `json:"outputFormat"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		if h.writeBodyTooLarge(c, err) {
			return
		}
		response.WriteErrorResp(c, http.StatusBadRequest, "请求格式不正确")
		return
	}
	session, err := h.chat.PrepareRegeneration(c.Request.Context(), currentUserID(c), c.Param("conversationID"), c.Param("messageID"), service.RegenerateInput{
		Provider: input.Provider, Model: input.Model, Temperature: input.Temperature,
		OutputFormat: input.OutputFormat, IdempotencyKey: c.GetHeader("Idempotency-Key"),
	})
	if !h.handlePrepareError(c, err) {
		return
	}
	h.streamPreparedSession(c, flusher, session)
}

func (h *Handler) EditMessage(c *gin.Context) {
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		response.WriteErrorResp(c, http.StatusInternalServerError, "服务器不支持流式响应")
		return
	}
	var input struct {
		Content          string   `json:"content"`
		Provider         string   `json:"provider"`
		Model            string   `json:"model"`
		Temperature      *float64 `json:"temperature"`
		OutputFormat     string   `json:"outputFormat"`
		KnowledgeBaseIDs []string `json:"knowledgeBaseIds"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		if h.writeBodyTooLarge(c, err) {
			return
		}
		response.WriteErrorResp(c, http.StatusBadRequest, "请求格式不正确")
		return
	}
	session, err := h.chat.PrepareEdit(c.Request.Context(), currentUserID(c), c.Param("conversationID"), c.Param("messageID"), service.ChatInput{
		Content: input.Content, Provider: input.Provider, Model: input.Model, Temperature: input.Temperature,
		OutputFormat: input.OutputFormat, KnowledgeBaseIDs: input.KnowledgeBaseIDs,
		IdempotencyKey: c.GetHeader("Idempotency-Key"),
	})
	if !h.handlePrepareError(c, err) {
		return
	}
	h.streamPreparedSession(c, flusher, session)
}

func (h *Handler) streamPreparedSession(c *gin.Context, flusher http.Flusher, session *service.ChatSession) {
	c.Header("Content-Type", "text/event-stream; charset=utf-8")
	c.Header("Cache-Control", "no-cache, no-transform")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	messageID := session.AssistantMessage.ID
	h.streams.Start(messageID, currentUserID(c))
	h.appendAndWriteStreamEvent(c.Writer, flusher, messageID, "metadata", map[string]any{
		"userMessage": session.UserMessage, "assistantMessage": session.AssistantMessage,
		"regeneration": session.IsRegeneration, "edit": session.IsEdit,
	}, false)

	var modelErr error
	completed, status, err := h.chat.Stream(c.Request.Context(), session, func(event provider.StreamEvent) {
		if event.Err != nil {
			modelErr = event.Err
			return
		}
		if event.ThinkDelta != "" {
			h.appendAndWriteStreamEvent(c.Writer, flusher, messageID, "think", map[string]any{"content": event.ThinkDelta, "modelSequence": event.Sequence}, false)
		}
		if event.Delta != "" {
			h.appendAndWriteStreamEvent(c.Writer, flusher, messageID, "delta", map[string]any{"content": event.Delta, "modelSequence": event.Sequence}, false)
		}
	})
	if err != nil {
		logger.Error(c.Request.Context(), "finalize streamed message failed", logger.Err(err))
		h.appendAndWriteStreamEvent(c.Writer, flusher, messageID, "error", map[string]any{"message": "保存消息终态失败"}, true)
		return
	}
	h.logChatCompleted(c, completed, status)
	if status == "complete" {
		h.titles.Schedule(c.Request.Context(), session.UserID, session.AssistantMessage.ConversationID)
	}
	// Citations are a separate cached SSE event so reconnect replay preserves
	// the exact metadata associated with the finalized answer.
	h.appendAndWriteStreamEvent(c.Writer, flusher, messageID, "citations", map[string]any{"citations": completed.Citations}, false)
	switch status {
	case "complete":
		h.appendAndWriteStreamEvent(c.Writer, flusher, messageID, "done", map[string]any{"assistantMessage": completed}, true)
	case "stopped", "interrupted":
		h.appendAndWriteStreamEvent(c.Writer, flusher, messageID, "interrupted", map[string]any{
			"assistantMessage": completed, "reason": completed.FinishReason,
		}, true)
	default:
		message := "模型调用失败"
		if modelErr != nil {
			message += "：" + safeError(modelErr)
		}
		h.appendAndWriteStreamEvent(c.Writer, flusher, messageID, "error", map[string]any{
			"message": message, "assistantMessage": completed,
		}, true)
	}
}

func (h *Handler) ResumeMessageStream(c *gin.Context) {
	afterValue := c.Query("afterSequence")
	if afterValue == "" {
		afterValue = c.GetHeader("Last-Event-ID")
	}
	if afterValue == "" {
		afterValue = "0"
	}
	after, err := strconv.ParseInt(afterValue, 10, 64)
	if err != nil || after < 0 {
		response.WriteErrorResp(c, http.StatusBadRequest, "afterSequence 必须是非负整数")
		return
	}
	messageID := c.Param("messageID")
	snapshot := h.streams.Snapshot(messageID, currentUserID(c), after)
	if !snapshot.Found {
		response.WriteErrorResp(c, http.StatusNotFound, "流式缓存不存在或已过期")
		return
	}
	if snapshot.Gap {
		response.WriteErrorResp(c, http.StatusConflict, "流式缓存片段已过期，请重新加载消息")
		return
	}
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		response.WriteErrorResp(c, http.StatusInternalServerError, "服务器不支持流式响应")
		return
	}
	c.Header("Content-Type", "text/event-stream; charset=utf-8")
	c.Header("Cache-Control", "no-cache, no-transform")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()

	for {
		for _, event := range snapshot.Events {
			writeCachedSSE(c.Writer, flusher, event.Sequence, event.Name, event.Data)
			after = event.Sequence
		}
		snapshot.Events = nil
		if snapshot.Terminal {
			return
		}
		select {
		case <-c.Request.Context().Done():
			return
		case <-snapshot.Wait:
			snapshot = h.streams.Snapshot(messageID, currentUserID(c), after)
			if !snapshot.Found || snapshot.Gap {
				return
			}
		case <-keepalive.C:
			_, _ = fmt.Fprint(c.Writer, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

// ChatMessage returns the complete answer in one JSON response. It has the
// same provider, tool-calling, usage and persistence behaviour as StreamMessage.
func (h *Handler) ChatMessage(c *gin.Context) {
	session, ok := h.prepareChatSession(c)
	if !ok {
		return
	}
	completed, err := h.chat.Chat(c.Request.Context(), session)
	if err != nil {
		logger.Error(c.Request.Context(), "non-streaming model chat failed", logger.Err(err))
		if errors.Is(err, llmgateway.ErrRateLimitExceeded) {
			response.WriteErrorResp(c, http.StatusTooManyRequests, err.Error())
			return
		}
		if errors.Is(err, llmgateway.ErrTokenBudgetExceeded) {
			response.WriteErrorResp(c, http.StatusUnprocessableEntity, err.Error())
			return
		}
		if errors.Is(err, llmgateway.ErrStructuredOutputInvalid) {
			response.WriteErrorResp(c, http.StatusBadGateway, err.Error())
			return
		}
		response.WriteErrorResp(c, http.StatusBadGateway, "模型调用失败")
		return
	}
	h.logChatCompleted(c, completed, completed.Status)
	if completed.Status == "complete" {
		h.titles.Schedule(c.Request.Context(), session.UserID, session.AssistantMessage.ConversationID)
	}
	response.WriteOKResp(c, gin.H{"userMessage": session.UserMessage, "assistantMessage": completed})
}

func (h *Handler) prepareChatSession(c *gin.Context) (*service.ChatSession, bool) {
	var input struct {
		Content          string   `json:"content"`
		Provider         string   `json:"provider"`
		Model            string   `json:"model"`
		Temperature      *float64 `json:"temperature"`
		OutputFormat     string   `json:"outputFormat"`
		AttachmentIDs    []string `json:"attachmentIds"`
		KnowledgeBaseIDs []string `json:"knowledgeBaseIds"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		if h.writeBodyTooLarge(c, err) {
			return nil, false
		}
		response.WriteErrorResp(c, http.StatusBadRequest, "请求格式不正确")
		return nil, false
	}
	session, err := h.chat.Prepare(c.Request.Context(), currentUserID(c), c.Param("conversationID"), service.ChatInput{
		Content:          input.Content,
		Provider:         input.Provider,
		Model:            input.Model,
		Temperature:      input.Temperature,
		OutputFormat:     input.OutputFormat,
		AttachmentIDs:    input.AttachmentIDs,
		KnowledgeBaseIDs: input.KnowledgeBaseIDs,
		IdempotencyKey:   c.GetHeader("Idempotency-Key"),
	})
	return session, h.handlePrepareError(c, err)
}

func (h *Handler) handlePrepareError(c *gin.Context, err error) bool {
	var providerSelectionError *service.ProviderSelectionError
	switch {
	case err == nil:
		return true
	case errors.Is(err, service.ErrInvalidMessageLength):
		response.WriteErrorResp(c, http.StatusBadRequest, "消息长度超过限制，请缩短内容后重试")
	case errors.Is(err, service.ErrContentBlockedByModeration):
		response.WriteErrorResp(c, http.StatusForbidden, "内容可能包含敏感信息，无法发送")
	case errors.Is(err, service.ErrDailyQuotaExceeded):
		response.WriteErrorResp(c, http.StatusTooManyRequests, "今日额度已用完，请明天再试")
	case errors.Is(err, service.ErrMonthlyQuotaExceeded):
		response.WriteErrorResp(c, http.StatusTooManyRequests, "本月额度已用完，请下月再试")
	case errors.Is(err, service.ErrTooManyConcurrentGenerations):
		response.WriteErrorResp(c, http.StatusTooManyRequests, "同时生成的请求过多，请等待当前回答完成")
	case errors.Is(err, service.ErrInvalidTemperature):
		response.WriteErrorResp(c, http.StatusBadRequest, "temperature 必须在 0 到 2 之间")
	case errors.Is(err, service.ErrKnowledgeQueryRequired):
		response.WriteErrorResp(c, http.StatusBadRequest, "使用知识库时必须输入问题")
	case errors.Is(err, service.ErrKnowledgeQueryTooLong):
		response.WriteErrorResp(c, http.StatusBadRequest, "知识库检索问题过长")
	case errors.Is(err, service.ErrTooManyKnowledgeBases):
		response.WriteErrorResp(c, http.StatusBadRequest, "选择的知识库数量超过限制")
	case errors.Is(err, service.ErrInvalidKnowledgeBaseID):
		response.WriteErrorResp(c, http.StatusBadRequest, "知识库 ID 格式不正确")
	case errors.Is(err, service.ErrKnowledgeBaseNotFound):
		response.WriteErrorResp(c, http.StatusNotFound, "知识库不存在或无权访问")
	case errors.Is(err, service.ErrEmbeddingNotConfigured):
		response.WriteErrorResp(c, http.StatusServiceUnavailable, "知识库检索暂不可用")
	case errors.Is(err, service.ErrKnowledgeRetrievalFailed):
		response.WriteErrorResp(c, http.StatusBadGateway, "知识库检索失败")
	case errors.Is(err, service.ErrOutputFormatNotFound):
		response.WriteErrorResp(c, http.StatusBadRequest, "不支持的输出格式")
	case errors.Is(err, llmgateway.ErrInvalidResponseFormat):
		response.WriteErrorResp(c, http.StatusBadRequest, err.Error())
	case errors.Is(err, llmgateway.ErrTokenBudgetExceeded):
		response.WriteErrorResp(c, http.StatusUnprocessableEntity, "当前消息超过模型可用上下文，请缩短内容后重试")
	case errors.Is(err, service.ErrConversationNotFound):
		response.WriteErrorResp(c, http.StatusNotFound, "对话不存在")
	case errors.Is(err, service.ErrMessageNotFound):
		response.WriteErrorResp(c, http.StatusNotFound, "回答不存在")
	case errors.Is(err, service.ErrMessageNotRegenerable):
		response.WriteErrorResp(c, http.StatusConflict, "当前回答不能重新生成")
	case errors.Is(err, service.ErrMessageNotEditable):
		response.WriteErrorResp(c, http.StatusConflict, "当前消息不能编辑")
	case errors.Is(err, service.ErrTooManyAttachments):
		response.WriteErrorResp(c, http.StatusBadRequest, "单条消息的附件数量超过限制")
	case errors.Is(err, service.ErrAttachmentNotFound):
		response.WriteErrorResp(c, http.StatusNotFound, "附件不存在或无权访问")
	case errors.Is(err, service.ErrAttachmentNotReady):
		response.WriteErrorResp(c, http.StatusConflict, "附件尚未解析完成，请稍后重试")
	case errors.Is(err, service.ErrAttachmentAlreadyUsed):
		response.WriteErrorResp(c, http.StatusConflict, "附件已经用于其他消息")
	case errors.Is(err, service.ErrModelDoesNotSupportVision):
		response.WriteErrorResp(c, http.StatusBadRequest, "当前模型不支持图片输入，请切换视觉模型")
	case errors.Is(err, llmgateway.ErrIdempotencyInProgress):
		response.WriteErrorResp(c, http.StatusConflict, "相同消息正在处理，请等待本次请求完成")
	case errors.Is(err, llmgateway.ErrIdempotencyCompleted):
		response.WriteErrorResp(c, http.StatusConflict, "相同消息已经处理完成，请刷新会话查看结果")
	case errors.As(err, &providerSelectionError):
		response.WriteErrorResp(c, http.StatusBadRequest, providerSelectionError.Error())
	case err != nil:
		logger.Error(c.Request.Context(), "prepare message stream failed", logger.Err(err))
		response.WriteErrorResp(c, http.StatusInternalServerError, "服务器内部错误")
	}
	return false
}

func (h *Handler) logChatCompleted(c *gin.Context, completed model.Message, status string) {
	logger.Info(c.Request.Context(), "model chat completed",
		logger.String("conversation_id", c.Param("conversationID")),
		logger.String("assistant_message_id", completed.ID),
		logger.String("provider", completed.Provider),
		logger.String("model", completed.Model),
		logger.String("status", status),
		logger.String("finish_reason", completed.FinishReason),
		logger.Int64("input_tokens", completed.InputTokens),
		logger.Int64("output_tokens", completed.OutputTokens),
		logger.Int64("total_tokens", completed.TotalTokens),
		logger.Bool("usage_reported", completed.UsageReported),
		logger.Int64("cost_nano_usd", completed.CostNanoUSD),
		logger.Bool("cost_known", completed.CostKnown),
		logger.Bool("is_regeneration", completed.RegeneratedFrom != nil),
		logger.Int64("response_version", completed.ResponseVersion),
	)
}

// 做两件事：先缓存，再推给当前客户端
func (h *Handler) appendAndWriteStreamEvent(w http.ResponseWriter, flusher http.Flusher, messageID, name string, payload map[string]any, terminal bool) {
	// 写入 streamcache，分配递增 sequence，带上 messageId，并标记是否终态（done / error / interrupted 时 terminal=true）。这样断线后可通过 ResumeMessageStream 按 afterSequence 续传
	event, ok := h.streams.Append(messageID, name, payload, terminal)
	if !ok {
		return
	}
	//把同一条事件写成 SSE（id / event / data）并 Flush，当前连接立刻收到。
	writeCachedSSE(w, flusher, event.Sequence, event.Name, event.Data)
}

func writeCachedSSE(w http.ResponseWriter, flusher http.Flusher, sequence int64, name string, data []byte) {
	_, _ = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", sequence, name, data)
	flusher.Flush()
}

// writeBodyTooLarge 识别请求体超过 MaxBodyBytes 限制的绑定错误，
// 以 413 响应；未命中时返回 false 由调用方继续常规错误处理。
func (h *Handler) writeBodyTooLarge(c *gin.Context, err error) bool {
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		response.WriteErrorResp(c, http.StatusRequestEntityTooLarge, "请求内容超过大小限制")
		return true
	}
	return false
}
