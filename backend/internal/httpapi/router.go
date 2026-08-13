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
	protected.GET("/account/quota", s.handlers.GetMyQuota)
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

	// 生成类接口叠加请求体大小限制和按用户限流：
	// 限流防瞬时洪峰，额度与并发限制在 service 层准入时兼顾。
	generation := protected.Group("")
	generation.Use(middleware.MaxBodyBytes(s.chatBodyMaxBytes))
	generation.Use(middleware.UserRateLimit(s.authLimiter, s.alerter, "chat", s.chatRateLimit, s.chatRateLimitWin))
	generation.POST("/conversations/:conversationID/messages/chat", s.handlers.ChatMessage)
	generation.POST("/conversations/:conversationID/messages", s.handlers.StreamMessage)
	generation.POST("/conversations/:conversationID/messages/:messageID/regenerate", s.handlers.RegenerateMessage)
	generation.POST("/conversations/:conversationID/messages/:messageID/edit", s.handlers.EditMessage)
	protected.PATCH("/conversations/:conversationID/messages/:messageID/activate", s.handlers.ActivateBranch)
	protected.GET("/messages/:messageID/stream", s.handlers.ResumeMessageStream)

	// 管理员运营接口：封禁/解封和额度调整，全部写入审计日志。
	admin := protected.Group("/admin")
	admin.Use(middleware.RequireAdmin(s.admin))
	admin.PATCH("/users/:userID/ban", s.handlers.BanUser)
	admin.PATCH("/users/:userID/unban", s.handlers.UnbanUser)
	admin.PATCH("/users/:userID/quota", s.handlers.AdjustUserQuota)
	admin.GET("/users/:userID/quota", s.handlers.GetUserQuota)

	return engine
}
