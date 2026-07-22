// Package handler translates HTTP requests into service calls and HTTP responses.
package handler

import (
	"github.com/example/agent-platform/backend/internal/service"
	"github.com/example/agent-platform/backend/internal/streamcache"
)

type Handler struct {
	auth          *service.AuthService
	conversations *service.ConversationService
	chat          *service.ChatService
	catalog       *service.CatalogService
	streams       *streamcache.Cache
}

func New(auth *service.AuthService, conversations *service.ConversationService, chat *service.ChatService, catalog *service.CatalogService, streams *streamcache.Cache) *Handler {
	return &Handler{auth: auth, conversations: conversations, chat: chat, catalog: catalog, streams: streams}
}
