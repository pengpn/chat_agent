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
}

func New(auth *service.AuthService, conversations *service.ConversationService, chat *service.ChatService, titles *service.TitleService, catalog *service.CatalogService, streams *streamcache.Cache, events *userevent.Hub) *Handler {
	return &Handler{auth: auth, conversations: conversations, chat: chat, titles: titles, catalog: catalog, streams: streams, events: events}
}
