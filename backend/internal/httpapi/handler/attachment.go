package handler

import (
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/example/agent-platform/backend/internal/httpapi/response"
	"github.com/example/agent-platform/backend/internal/logger"
	"github.com/example/agent-platform/backend/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *Handler) UploadAttachment(c *gin.Context) {
	if h.attachments == nil {
		response.WriteErrorResp(c, http.StatusServiceUnavailable, "附件服务未配置")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, h.attachments.MaxBytes()+(1<<20))
	fileHeader, err := c.FormFile("file")
	if err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			response.WriteErrorResp(c, http.StatusRequestEntityTooLarge, "文件超过允许的大小")
			return
		}
		response.WriteErrorResp(c, http.StatusBadRequest, "请选择要上传的文件")
		return
	}
	file, err := fileHeader.Open()
	if err != nil {
		response.WriteErrorResp(c, http.StatusBadRequest, "无法读取上传文件")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, h.attachments.MaxBytes()+1))
	if err != nil {
		response.WriteErrorResp(c, http.StatusBadRequest, "无法读取上传文件")
		return
	}
	item, err := h.attachments.Upload(c.Request.Context(), currentUserID(c), c.Param("conversationID"), fileHeader.Filename, data)
	if h.writeAttachmentError(c, err) {
		return
	}
	response.WriteCreatedResp(c, gin.H{"attachment": item})
}

func (h *Handler) RetryAttachment(c *gin.Context) {
	item, err := h.attachments.Retry(c.Request.Context(), currentUserID(c), c.Param("conversationID"), c.Param("attachmentID"))
	if h.writeAttachmentError(c, err) {
		return
	}
	response.WriteOKResp(c, gin.H{"attachment": item})
}

func (h *Handler) DeleteAttachment(c *gin.Context) {
	err := h.attachments.DeletePending(c.Request.Context(), currentUserID(c), c.Param("conversationID"), c.Param("attachmentID"))
	if h.writeAttachmentError(c, err) {
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) DownloadAttachment(c *gin.Context) {
	item, data, err := h.attachments.Content(c.Request.Context(), currentUserID(c), c.Param("conversationID"), c.Param("attachmentID"))
	if h.writeAttachmentError(c, err) {
		return
	}
	c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": item.OriginalName}))
	c.Data(http.StatusOK, item.MIMEType, data)
}

func (h *Handler) writeAttachmentError(c *gin.Context, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, service.ErrConversationNotFound), errors.Is(err, service.ErrAttachmentNotFound):
		response.WriteErrorResp(c, http.StatusNotFound, "附件或对话不存在")
	case errors.Is(err, service.ErrAttachmentTooLarge):
		response.WriteErrorResp(c, http.StatusRequestEntityTooLarge, "文件超过允许的大小")
	case errors.Is(err, service.ErrAttachmentEmpty):
		response.WriteErrorResp(c, http.StatusBadRequest, "不能上传空文件")
	case errors.Is(err, service.ErrAttachmentTypeNotAllowed):
		response.WriteErrorResp(c, http.StatusUnsupportedMediaType, "仅支持 PNG、JPG、GIF、WebP、TXT、PDF 和 DOCX")
	case errors.Is(err, service.ErrAttachmentMIMEMismatch):
		response.WriteErrorResp(c, http.StatusUnsupportedMediaType, "文件扩展名与实际内容类型不一致")
	case errors.Is(err, service.ErrAttachmentAlreadyUsed):
		response.WriteErrorResp(c, http.StatusConflict, "附件已用于一条消息，不能再次修改")
	default:
		logger.Error(c.Request.Context(), "attachment operation failed", logger.Err(err))
		response.WriteErrorResp(c, http.StatusInternalServerError, "附件处理失败")
	}
	return true
}
