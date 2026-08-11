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
	refreshCookie AuthCookieConfig
}

type AuthCookieConfig struct {
	Name   string
	Secure bool
}

func New(auth *service.AuthService, conversations *service.ConversationService, chat *service.ChatService, titles *service.TitleService, catalog *service.CatalogService, streams *streamcache.Cache, events *userevent.Hub, attachmentServices ...*service.AttachmentService) *Handler {
	var attachments *service.AttachmentService
	if len(attachmentServices) > 0 {
		attachments = attachmentServices[0]
	}
	return &Handler{auth: auth, conversations: conversations, chat: chat, titles: titles, catalog: catalog, streams: streams, events: events, attachments: attachments}
}

func (h *Handler) ConfigureAuthCookies(config AuthCookieConfig) {
	h.refreshCookie = config
	if h.refreshCookie.Name == "" {
		h.refreshCookie.Name = "agent_refresh"
	}
}
