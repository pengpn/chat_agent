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

func (h *Handler) CreateKnowledgeBase(c *gin.Context) {
	var input struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		response.WriteErrorResp(c, http.StatusBadRequest, "请求格式不正确")
		return
	}
	base, err := h.knowledge.CreateBase(c.Request.Context(), currentUserID(c), input.Name, input.Description)
	if h.writeKnowledgeError(c, err) {
		return
	}
	response.WriteCreatedResp(c, gin.H{"knowledgeBase": base})
}

func (h *Handler) ListKnowledgeBases(c *gin.Context) {
	items, err := h.knowledge.ListBases(c.Request.Context(), currentUserID(c))
	if h.writeKnowledgeError(c, err) {
		return
	}
	response.WriteOKResp(c, gin.H{"knowledgeBases": items})
}

func (h *Handler) GetKnowledgeBase(c *gin.Context) {
	item, err := h.knowledge.GetBase(c.Request.Context(), currentUserID(c), c.Param("knowledgeBaseID"))
	if h.writeKnowledgeError(c, err) {
		return
	}
	response.WriteOKResp(c, gin.H{"knowledgeBase": item})
}

func (h *Handler) UpdateKnowledgeBase(c *gin.Context) {
	var input struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		response.WriteErrorResp(c, http.StatusBadRequest, "请求格式不正确")
		return
	}
	item, err := h.knowledge.UpdateBase(c.Request.Context(), currentUserID(c), c.Param("knowledgeBaseID"), input.Name, input.Description)
	if h.writeKnowledgeError(c, err) {
		return
	}
	response.WriteOKResp(c, gin.H{"knowledgeBase": item})
}

func (h *Handler) DeleteKnowledgeBase(c *gin.Context) {
	if h.writeKnowledgeError(c, h.knowledge.DeleteBase(c.Request.Context(), currentUserID(c), c.Param("knowledgeBaseID"))) {
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) UploadKnowledgeDocument(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, h.knowledge.MaxBytes()+(1<<20))
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
	data, err := io.ReadAll(io.LimitReader(file, h.knowledge.MaxBytes()+1))
	if err != nil {
		response.WriteErrorResp(c, http.StatusBadRequest, "无法读取上传文件")
		return
	}
	result, err := h.knowledge.Upload(c.Request.Context(), currentUserID(c), c.Param("knowledgeBaseID"), fileHeader.Filename, data)
	if h.writeKnowledgeError(c, err) {
		return
	}
	if result.Created {
		response.WriteCreatedResp(c, result)
		return
	}
	response.WriteOKResp(c, result)
}

func (h *Handler) ListKnowledgeDocuments(c *gin.Context) {
	items, err := h.knowledge.ListDocuments(c.Request.Context(), currentUserID(c), c.Param("knowledgeBaseID"))
	if h.writeKnowledgeError(c, err) {
		return
	}
	response.WriteOKResp(c, gin.H{"documents": items})
}

func (h *Handler) GetKnowledgeDocument(c *gin.Context) {
	item, err := h.knowledge.GetDocument(c.Request.Context(), currentUserID(c), c.Param("knowledgeBaseID"), c.Param("documentID"))
	if h.writeKnowledgeError(c, err) {
		return
	}
	response.WriteOKResp(c, gin.H{"document": item})
}

func (h *Handler) RetryKnowledgeDocument(c *gin.Context) {
	item, err := h.knowledge.Retry(c.Request.Context(), currentUserID(c), c.Param("knowledgeBaseID"), c.Param("documentID"))
	if h.writeKnowledgeError(c, err) {
		return
	}
	response.WriteOKResp(c, gin.H{"document": item})
}

func (h *Handler) ReindexKnowledgeDocument(c *gin.Context) {
	item, err := h.knowledge.Reindex(c.Request.Context(), currentUserID(c), c.Param("knowledgeBaseID"), c.Param("documentID"))
	if h.writeKnowledgeError(c, err) {
		return
	}
	response.WriteOKResp(c, gin.H{"document": item})
}

func (h *Handler) ListKnowledgeChunks(c *gin.Context) {
	items, err := h.knowledge.ListChunks(c.Request.Context(), currentUserID(c), c.Param("knowledgeBaseID"), c.Param("documentID"))
	if h.writeKnowledgeError(c, err) {
		return
	}
	response.WriteOKResp(c, gin.H{"chunks": items})
}

func (h *Handler) DownloadKnowledgeDocument(c *gin.Context) {
	item, data, err := h.knowledge.Content(c.Request.Context(), currentUserID(c), c.Param("knowledgeBaseID"), c.Param("documentID"))
	if h.writeKnowledgeError(c, err) {
		return
	}
	c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": item.OriginalName}))
	c.Data(http.StatusOK, item.MIMEType, data)
}

func (h *Handler) DeleteKnowledgeDocument(c *gin.Context) {
	err := h.knowledge.DeleteDocument(c.Request.Context(), currentUserID(c), c.Param("knowledgeBaseID"), c.Param("documentID"))
	if h.writeKnowledgeError(c, err) {
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) writeKnowledgeError(c *gin.Context, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, service.ErrKnowledgeBaseNotFound):
		response.WriteErrorResp(c, http.StatusNotFound, "知识库不存在")
	case errors.Is(err, service.ErrKnowledgeDocumentNotFound):
		response.WriteErrorResp(c, http.StatusNotFound, "知识文档不存在")
	case errors.Is(err, service.ErrKnowledgeNameRequired):
		response.WriteErrorResp(c, http.StatusBadRequest, "知识库名称不能为空")
	case errors.Is(err, service.ErrKnowledgeNameTooLong):
		response.WriteErrorResp(c, http.StatusBadRequest, "知识库名称不能超过 120 个字符")
	case errors.Is(err, service.ErrKnowledgeDescriptionLong):
		response.WriteErrorResp(c, http.StatusBadRequest, "知识库描述不能超过 2000 个字符")
	case errors.Is(err, service.ErrKnowledgeDocumentBusy):
		response.WriteErrorResp(c, http.StatusConflict, "文档当前状态不允许此操作")
	case errors.Is(err, service.ErrEmbeddingNotConfigured):
		response.WriteErrorResp(c, http.StatusServiceUnavailable, "Embedding 服务尚未配置")
	case errors.Is(err, service.ErrAttachmentTooLarge):
		response.WriteErrorResp(c, http.StatusRequestEntityTooLarge, "文件超过允许的大小")
	case errors.Is(err, service.ErrAttachmentEmpty):
		response.WriteErrorResp(c, http.StatusBadRequest, "不能上传空文件")
	case errors.Is(err, service.ErrAttachmentTypeNotAllowed):
		response.WriteErrorResp(c, http.StatusUnsupportedMediaType, "知识库仅支持 TXT、PDF 和 DOCX")
	case errors.Is(err, service.ErrAttachmentMIMEMismatch):
		response.WriteErrorResp(c, http.StatusUnsupportedMediaType, "文件扩展名与实际内容类型不一致")
	default:
		logger.Error(c.Request.Context(), "knowledge operation failed", logger.Err(err))
		response.WriteErrorResp(c, http.StatusInternalServerError, "知识库操作失败")
	}
	return true
}
