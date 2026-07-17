package handler

import (
	"github.com/example/agent-platform/backend/internal/httpapi/response"
	"github.com/gin-gonic/gin"
)

func (h *Handler) Healthz(c *gin.Context) {
	response.WriteOKResp(c, gin.H{"status": "ok"})
}

func (h *Handler) Models(c *gin.Context) {
	response.WriteOKResp(c, gin.H{"providers": h.catalog.Providers()})
}
