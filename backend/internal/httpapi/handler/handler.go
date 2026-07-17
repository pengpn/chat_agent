// Package handler translates HTTP requests into service calls and HTTP responses.
package handler

import "github.com/example/agent-platform/backend/internal/service"

type Handler struct {
	auth          *service.AuthService
	conversations *service.ConversationService
	chat          *service.ChatService
	catalog       *service.CatalogService
}

func New(auth *service.AuthService, conversations *service.ConversationService, chat *service.ChatService, catalog *service.CatalogService) *Handler {
	return &Handler{auth: auth, conversations: conversations, chat: chat, catalog: catalog}
}
