package service

import "github.com/example/agent-platform/backend/internal/provider"

type CatalogService struct{ providers *provider.Registry }

func NewCatalog(providers *provider.Registry) *CatalogService {
	return &CatalogService{providers: providers}
}

func (s *CatalogService) Providers() []provider.PublicProvider { return s.providers.Public() }
