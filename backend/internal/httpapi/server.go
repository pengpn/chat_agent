package httpapi

import (
	"context"
	"time"

	"github.com/example/agent-platform/backend/internal/alerting"
	"github.com/example/agent-platform/backend/internal/auth"
	"github.com/example/agent-platform/backend/internal/config"
	"github.com/example/agent-platform/backend/internal/embeddinggateway"
	"github.com/example/agent-platform/backend/internal/httpapi/handler"
	"github.com/example/agent-platform/backend/internal/llmgateway"
	"github.com/example/agent-platform/backend/internal/mailer"
	"github.com/example/agent-platform/backend/internal/moderation"
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
	admin              *service.AdminService
	knowledge          *service.KnowledgeService
	authLimiter        ratelimit.Limiter
	alerter            alerting.Alerter
	chatRateLimit      int
	chatRateLimitWin   time.Duration
	chatBodyMaxBytes   int64
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
	return NewServerWithEmbeddingGateway(repositories, jwtManager, providers, llmLimiter, authLimiter, store, emailSender, nil, cfg)
}

// NewServerWithEmbeddingGateway is the production composition root for RAG.
// The older constructor remains available to keep focused HTTP tests simple.
func NewServerWithEmbeddingGateway(repositories *repository.Repositories, jwtManager *auth.JWTManager, providers *provider.Registry, llmLimiter llmgateway.RateLimiter, authLimiter ratelimit.Limiter, store objectstore.Store, emailSender mailer.Sender, embeddings embeddinggateway.Gateway, cfg config.Config) *Server {
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
	quotas := service.NewQuota(repositories.Quotas, service.QuotaConfig{
		Enabled:                  cfg.QuotaEnabled,
		DefaultDailyTokens:       cfg.QuotaDefaultDailyTokens,
		DefaultMonthlyTokens:     cfg.QuotaDefaultMonthlyTokens,
		MaxConcurrentGenerations: cfg.QuotaMaxConcurrent,
	})
	adminService := service.NewAdmin(repositories.Users, repositories.Auth, quotas)
	alerter := alerting.NewZap()
	tools := tool.NewDefaultRegistry()
	tools.Configure(tool.SafetyConfig{
		Policy:           tool.NewRolePolicy(adminService, cfg.ToolAdminOnly),
		MaxArgumentBytes: cfg.ToolArgumentMaxBytes,
		MaxResultBytes:   cfg.ToolResultMaxBytes,
		Timeout:          cfg.ToolExecutionTimeout,
	})
	authService := service.NewAuth(repositories.Auth, jwtManager, emailSender, authLimiter, store, service.AuthConfig{
		RefreshTokenTTL: cfg.RefreshTokenTTL, EmailVerificationTTL: cfg.EmailVerificationTTL,
		PasswordResetTTL: cfg.PasswordResetTTL, PublicWebURL: cfg.PublicWebURL,
		RateLimitWindow: cfg.AuthRateLimitWindow, LoginRateLimit: cfg.AuthLoginRateLimit,
		RegisterRateLimit: cfg.AuthRegisterRateLimit, PasswordResetRateLimit: cfg.AuthPasswordResetRateLimit,
		AdminEmails: cfg.AdminEmails,
	})
	chatService := service.NewChat(repositories, gateway, tools, outputformat.NewDefaultRegistry(), cfg.MaxToolIterations, &provider.ThinkingConfig{
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
	}, attachments)
	chatService.ConfigureSafety(service.SafetyDependencies{
		Quotas:           quotas,
		Moderator:        moderation.New(cfg.ModerationEnabled, cfg.ModerationInputBlocklist, cfg.ModerationOutputBlocklist),
		Alerts:           alerter,
		CostAlertNanoUSD: cfg.AlertCostThresholdNanoUSD,
		MaxMessageChars:  cfg.MessageMaxChars,
	})
	handlers := handler.New(
		authService,
		service.NewConversation(repositories.Conversations, repositories.Messages, attachments),
		chatService,
		service.NewTitle(repositories.Conversations, gateway, userEvents, service.TitleConfig{
			Provider: cfg.TitleGenerationProvider, Model: cfg.TitleGenerationModel,
			MaxOutputTokens: cfg.TitleGenerationMaxTokens, MaxLength: cfg.TitleGenerationMaxLength,
			Timeout: cfg.TitleGenerationTimeout,
		}),
		service.NewCatalog(providers),
		streams,
		userEvents,
		quotas,
		adminService,
		attachments,
	)
	handlers.ConfigureAuthCookies(handler.AuthCookieConfig{Name: cfg.RefreshCookieName, Secure: cfg.RefreshCookieSecure})
	estimator, err := embeddinggateway.NewTokenEstimator(cfg.Embedding.Tokenizer)
	if err != nil {
		estimator = embeddinggateway.HeuristicTokenEstimator{}
	}
	knowledgeService := service.NewKnowledge(repositories.Knowledge, store, embeddings, estimator, service.KnowledgeConfig{
		MaxBytes: cfg.Knowledge.DocumentMaxBytes, MaxTextChars: cfg.Knowledge.MaxTextChars,
		ChunkTargetTokens: cfg.Knowledge.ChunkTargetTokens, ChunkOverlap: cfg.Knowledge.ChunkOverlap,
		ChunkMinTokens: cfg.Knowledge.ChunkMinTokens, EmbeddingBatch: cfg.Knowledge.EmbeddingBatch,
		IngestionTimeout: cfg.Knowledge.IngestionTimeout,
	})
	handlers.ConfigureKnowledge(knowledgeService)
	retrievalService := service.NewKnowledgeRetrieval(repositories.Knowledge, embeddings, estimator, service.KnowledgeRetrievalConfig{
		CandidateLimit: cfg.Knowledge.RetrievalCandidateLimit, TopK: cfg.Knowledge.RetrievalTopK,
		SimilarityThreshold: cfg.Knowledge.RetrievalSimilarityThreshold,
		ContextTokenBudget:  cfg.Knowledge.RetrievalContextTokenBudget,
		MaxKnowledgeBases:   cfg.Knowledge.RetrievalMaxKnowledgeBases,
		MaxQueryChars:       cfg.Knowledge.RetrievalMaxQueryChars,
		TraceEnabled:        cfg.Knowledge.RetrievalTraceEnabled,
	})
	chatService.ConfigureKnowledgeRetrieval(retrievalService)
	handlers.ConfigureKnowledgeRetrieval(retrievalService)
	return &Server{
		handlers:           handlers,
		jwt:                jwtManager,
		auth:               authService,
		admin:              adminService,
		knowledge:          knowledgeService,
		authLimiter:        authLimiter,
		alerter:            alerter,
		chatRateLimit:      cfg.ChatRateLimit,
		chatRateLimitWin:   cfg.ChatRateLimitWindow,
		chatBodyMaxBytes:   cfg.ChatBodyMaxBytes,
		corsAllowedOrigins: cfg.CORSAllowedOrigins,
	}
}

// CleanupKnowledgeObjects retries transactional object-deletion outbox tasks.
func (s *Server) CleanupKnowledgeObjects(ctx context.Context) {
	if s.knowledge != nil {
		s.knowledge.DrainObjectCleanup(ctx)
	}
}
