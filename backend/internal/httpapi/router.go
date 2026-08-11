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
	api.POST("/auth/refresh", s.handlers.Refresh)
	api.POST("/auth/logout", s.handlers.Logout)
	api.POST("/auth/verify-email", s.handlers.VerifyEmail)
	api.POST("/auth/forgot-password", s.handlers.ForgotPassword)
	api.POST("/auth/reset-password", s.handlers.ResetPassword)

	protected := api.Group("")
	protected.Use(middleware.Authenticate(s.jwt, s.auth))
	protected.GET("/auth/me", s.handlers.Me)
	protected.POST("/auth/logout-all", s.handlers.LogoutAll)
	protected.GET("/auth/sessions", s.handlers.ListSessions)
	protected.DELETE("/auth/sessions/:sessionID", s.handlers.RevokeSession)
	protected.POST("/auth/resend-verification", s.handlers.ResendVerification)
	protected.POST("/auth/change-password", s.handlers.ChangePassword)
	protected.GET("/account/export", s.handlers.ExportAccount)
	protected.DELETE("/account", s.handlers.DeleteAccount)
	protected.GET("/models", s.handlers.Models)
	protected.GET("/events", s.handlers.UserEvents)
	protected.GET("/conversations", s.handlers.ListConversations)
	protected.POST("/conversations", s.handlers.CreateConversation)
	protected.GET("/conversations/:conversationID/messages", s.handlers.ListMessages)
	protected.PATCH("/conversations/:conversationID", s.handlers.UpdateConversation)
	protected.DELETE("/conversations/:conversationID", s.handlers.DeleteConversation)
	protected.POST("/conversations/:conversationID/attachments", s.handlers.UploadAttachment)
	protected.POST("/conversations/:conversationID/attachments/:attachmentID/retry", s.handlers.RetryAttachment)
	protected.GET("/conversations/:conversationID/attachments/:attachmentID/content", s.handlers.DownloadAttachment)
	protected.DELETE("/conversations/:conversationID/attachments/:attachmentID", s.handlers.DeleteAttachment)
	protected.POST("/conversations/:conversationID/messages/chat", s.handlers.ChatMessage)
	protected.POST("/conversations/:conversationID/messages", s.handlers.StreamMessage)
	protected.POST("/conversations/:conversationID/messages/:messageID/regenerate", s.handlers.RegenerateMessage)
	protected.POST("/conversations/:conversationID/messages/:messageID/edit", s.handlers.EditMessage)
	protected.PATCH("/conversations/:conversationID/messages/:messageID/activate", s.handlers.ActivateBranch)
	protected.GET("/messages/:messageID/stream", s.handlers.ResumeMessageStream)

	return engine
}
