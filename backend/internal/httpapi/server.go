package httpapi

import (
	"github.com/example/agent-platform/backend/internal/auth"
	"github.com/example/agent-platform/backend/internal/config"
	"github.com/example/agent-platform/backend/internal/httpapi/handler"
	"github.com/example/agent-platform/backend/internal/provider"
	"github.com/example/agent-platform/backend/internal/repository"
	"github.com/example/agent-platform/backend/internal/service"
	"github.com/example/agent-platform/backend/internal/streamcache"
	"github.com/example/agent-platform/backend/internal/tool"
)

// Server owns the HTTP dependencies. Route composition lives in router.go.
type Server struct {
	handlers           *handler.Handler
	jwt                *auth.JWTManager
	corsAllowedOrigins []string
}

func NewServer(repositories *repository.Repositories, jwtManager *auth.JWTManager, providers *provider.Registry, cfg config.Config) *Server {
	streams := streamcache.New(cfg.StreamCacheTTL, cfg.StreamCacheMaxEvents)
	return &Server{
		handlers: handler.New(
			service.NewAuth(repositories.Users, jwtManager),
			service.NewConversation(repositories.Conversations, repositories.Messages),
			service.NewChat(repositories, providers, tool.NewDefaultRegistry(), cfg.MaxToolIterations, &provider.ThinkingConfig{
				Enabled:      cfg.EnableThinking,
				BudgetTokens: cfg.ThinkingBudgetTokens,
			}, cfg.SystemPrompt, service.StreamConfig{
				TTFTTimeout:  cfg.StreamTTFTTimeout,
				TotalTimeout: cfg.StreamTotalTimeout,
			}),
			service.NewCatalog(providers),
			streams,
		),
		jwt:                jwtManager,
		corsAllowedOrigins: cfg.CORSAllowedOrigins,
	}
}
