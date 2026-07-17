package httpapi

import (
	"github.com/example/agent-platform/backend/internal/auth"
	"github.com/example/agent-platform/backend/internal/config"
	"github.com/example/agent-platform/backend/internal/httpapi/handler"
	"github.com/example/agent-platform/backend/internal/provider"
	"github.com/example/agent-platform/backend/internal/repository"
	"github.com/example/agent-platform/backend/internal/service"
)

// Server owns the HTTP dependencies. Route composition lives in router.go.
type Server struct {
	handlers           *handler.Handler
	jwt                *auth.JWTManager
	corsAllowedOrigins []string
}

func NewServer(repositories *repository.Repositories, jwtManager *auth.JWTManager, providers *provider.Registry, cfg config.Config) *Server {
	return &Server{
		handlers: handler.New(
			service.NewAuth(repositories.Users, jwtManager),
			service.NewConversation(repositories.Conversations, repositories.Messages),
			service.NewChat(repositories, providers, cfg.SystemPrompt),
			service.NewCatalog(providers),
		),
		jwt:                jwtManager,
		corsAllowedOrigins: cfg.CORSAllowedOrigins,
	}
}
