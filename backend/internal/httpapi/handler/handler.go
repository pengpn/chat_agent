// Package handler translates HTTP requests into service calls and HTTP responses.
package handler

import (
	"github.com/example/agent-platform/backend/internal/service"
	"github.com/example/agent-platform/backend/internal/streamcache"
	"github.com/example/agent-platform/backend/internal/userevent"
)

type Handler struct {
	auth          *service.AuthService
	conversations *service.ConversationService
	chat          *service.ChatService
	titles        *service.TitleService
	catalog       *service.CatalogService
	streams       *streamcache.Cache
	events        *userevent.Hub
	attachments   *service.AttachmentService
	quotas        *service.QuotaService
	admin         *service.AdminService
	knowledge     *service.KnowledgeService
	retrieval     *service.KnowledgeRetrievalService
	refreshCookie AuthCookieConfig
}

func (h *Handler) ConfigureKnowledge(service *service.KnowledgeService) {
	h.knowledge = service
}

func (h *Handler) ConfigureKnowledgeRetrieval(service *service.KnowledgeRetrievalService) {
	h.retrieval = service
}

type AuthCookieConfig struct {
	Name   string
	Secure bool
}

func New(auth *service.AuthService, conversations *service.ConversationService, chat *service.ChatService, titles *service.TitleService, catalog *service.CatalogService, streams *streamcache.Cache, events *userevent.Hub, quotas *service.QuotaService, admin *service.AdminService, attachmentServices ...*service.AttachmentService) *Handler {
	var attachments *service.AttachmentService
	if len(attachmentServices) > 0 {
		attachments = attachmentServices[0]
	}
	return &Handler{auth: auth, conversations: conversations, chat: chat, titles: titles, catalog: catalog, streams: streams, events: events, quotas: quotas, admin: admin, attachments: attachments}
}

func (h *Handler) ConfigureAuthCookies(config AuthCookieConfig) {
	h.refreshCookie = config
	if h.refreshCookie.Name == "" {
		h.refreshCookie.Name = "agent_refresh"
	}
}
