package httpapi

import (
	"github.com/example/agent-platform/backend/internal/auth"
	"github.com/example/agent-platform/backend/internal/config"
	"github.com/example/agent-platform/backend/internal/httpapi/handler"
	"github.com/example/agent-platform/backend/internal/llmgateway"
	"github.com/example/agent-platform/backend/internal/mailer"
	"github.com/example/agent-platform/backend/internal/objectstore"
	"github.com/example/agent-platform/backend/internal/outputformat"
	"github.com/example/agent-platform/backend/internal/provider"
	"github.com/example/agent-platform/backend/internal/ratelimit"
	"github.com/example/agent-platform/backend/internal/repository"
	"github.com/example/agent-platform/backend/internal/service"
	"github.com/example/agent-platform/backend/internal/streamcache"
	"github.com/example/agent-platform/backend/internal/tool"
	"github.com/example/agent-platform/backend/internal/userevent"
)

// Server owns the HTTP dependencies. Route composition lives in router.go.
type Server struct {
	handlers           *handler.Handler
	jwt                *auth.JWTManager
	auth               *service.AuthService
	corsAllowedOrigins []string
}

func NewServer(repositories *repository.Repositories, jwtManager *auth.JWTManager, providers *provider.Registry, cfg config.Config) *Server {
	return NewServerWithRateLimiter(repositories, jwtManager, providers, nil, cfg)
}

// NewServerWithRateLimiter keeps HTTP composition testable while production
// injects the Redis-backed limiter assembled in cmd/api.
func NewServerWithRateLimiter(repositories *repository.Repositories, jwtManager *auth.JWTManager, providers *provider.Registry, limiter llmgateway.RateLimiter, cfg config.Config) *Server {
	return NewServerWithObjectStore(repositories, jwtManager, providers, limiter, objectstore.NewMemory(), cfg)
}

func NewServerWithObjectStore(repositories *repository.Repositories, jwtManager *auth.JWTManager, providers *provider.Registry, limiter llmgateway.RateLimiter, store objectstore.Store, cfg config.Config) *Server {
	return NewServerWithDependencies(repositories, jwtManager, providers, limiter, ratelimit.Noop{}, store, mailer.NoopSender{}, cfg)
}

func NewServerWithDependencies(repositories *repository.Repositories, jwtManager *auth.JWTManager, providers *provider.Registry, llmLimiter llmgateway.RateLimiter, authLimiter ratelimit.Limiter, store objectstore.Store, emailSender mailer.Sender, cfg config.Config) *Server {
	streams := streamcache.New(cfg.StreamCacheTTL, cfg.StreamCacheMaxEvents)
	userEvents := userevent.New(cfg.UserEventTTL, cfg.UserEventMaxEvents)
	attachments := service.NewAttachment(repositories.Conversations, repositories.Attachments, store, providers, service.AttachmentConfig{
		MaxBytes: cfg.AttachmentMaxBytes, MaxCount: cfg.AttachmentMaxCount, MaxTextChars: cfg.AttachmentMaxTextChars,
	})
	gateway := llmgateway.New(
		providers,
		llmgateway.NewModelTokenEstimator(providers),
		llmLimiter,
		llmgateway.NewMemoryIdempotencyStore(cfg.LLMIdempotencyTTL),
		llmgateway.ZapObservation{},
		llmgateway.Config{
			TokenBudget: cfg.LLMTokenBudget, MaxOutputTokens: cfg.LLMMaxOutputTokens,
			SyncMaxRetries: cfg.LLMSyncMaxRetries, RetryBackoff: cfg.LLMRetryBackoff,
		},
	)
	authService := service.NewAuth(repositories.Auth, jwtManager, emailSender, authLimiter, store, service.AuthConfig{
		RefreshTokenTTL: cfg.RefreshTokenTTL, EmailVerificationTTL: cfg.EmailVerificationTTL,
		PasswordResetTTL: cfg.PasswordResetTTL, PublicWebURL: cfg.PublicWebURL,
		RateLimitWindow: cfg.AuthRateLimitWindow, LoginRateLimit: cfg.AuthLoginRateLimit,
		RegisterRateLimit: cfg.AuthRegisterRateLimit, PasswordResetRateLimit: cfg.AuthPasswordResetRateLimit,
	})
	handlers := handler.New(
		authService,
		service.NewConversation(repositories.Conversations, repositories.Messages, attachments),
		service.NewChat(repositories, gateway, tool.NewDefaultRegistry(), outputformat.NewDefaultRegistry(), cfg.MaxToolIterations, &provider.ThinkingConfig{
			Enabled:      cfg.EnableThinking,
			BudgetTokens: cfg.ThinkingBudgetTokens,
		}, cfg.SystemPrompt, service.StreamConfig{
			TTFTTimeout:  cfg.StreamTTFTTimeout,
			TotalTimeout: cfg.StreamTotalTimeout,
		}, service.ContextConfig{
			SummaryEnabled: cfg.ContextSummaryEnabled, SummaryProvider: cfg.ContextSummaryProvider, SummaryModel: cfg.ContextSummaryModel,
			SummaryMaxTokens: cfg.ContextSummaryMaxTokens, KeepRecentMessages: cfg.ContextKeepRecentMessages,
			SummaryMaxCalls: cfg.ContextSummaryMaxCalls, QualityEnabled: cfg.ContextQualityEnabled,
			QualityThreshold: cfg.ContextQualityThreshold, QualityMaxTokens: cfg.ContextQualityMaxTokens,
		}, attachments),
		service.NewTitle(repositories.Conversations, gateway, userEvents, service.TitleConfig{
			Provider: cfg.TitleGenerationProvider, Model: cfg.TitleGenerationModel,
			MaxOutputTokens: cfg.TitleGenerationMaxTokens, MaxLength: cfg.TitleGenerationMaxLength,
			Timeout: cfg.TitleGenerationTimeout,
		}),
		service.NewCatalog(providers),
		streams,
		userEvents,
		attachments,
	)
	handlers.ConfigureAuthCookies(handler.AuthCookieConfig{Name: cfg.RefreshCookieName, Secure: cfg.RefreshCookieSecure})
	return &Server{
		handlers:           handlers,
		jwt:                jwtManager,
		auth:               authService,
		corsAllowedOrigins: cfg.CORSAllowedOrigins,
	}
}
