package httpapi

import (
	"net/http"

	"github.com/example/agent-platform/backend/internal/httpapi/middleware"
	"github.com/gin-gonic/gin"
)

// Router builds the Gin engine and registers the public and authenticated API routes.
func (s *Server) Router() http.Handler {
	engine := gin.New()
	engine.Use(middleware.Recovery(), middleware.RequestID(), middleware.RequestLogger(), middleware.CORS(s.corsAllowedOrigins))
	engine.GET("/healthz", s.handlers.Healthz)

	api := engine.Group("/api/v1")
	api.POST("/auth/register", s.handlers.Register)
	api.POST("/auth/login", s.handlers.Login)

	protected := api.Group("")
	protected.Use(middleware.Authenticate(s.jwt))
	protected.GET("/auth/me", s.handlers.Me)
	protected.GET("/models", s.handlers.Models)
	protected.GET("/conversations", s.handlers.ListConversations)
	protected.POST("/conversations", s.handlers.CreateConversation)
	protected.GET("/conversations/:conversationID/messages", s.handlers.ListMessages)
	protected.PATCH("/conversations/:conversationID", s.handlers.UpdateConversation)
	protected.DELETE("/conversations/:conversationID", s.handlers.DeleteConversation)
	protected.POST("/conversations/:conversationID/messages/chat", s.handlers.ChatMessage)
	protected.POST("/conversations/:conversationID/messages", s.handlers.StreamMessage)
	protected.GET("/messages/:messageID/stream", s.handlers.ResumeMessageStream)

	return engine
}
