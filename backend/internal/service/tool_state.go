package service

import (
	"context"

	"github.com/example/agent-platform/backend/internal/database/model"
	"github.com/example/agent-platform/backend/internal/repository"
)

// ToolStateService 是网页的只读聚合查询，不实现 Tool，也不执行任何工具业务。
type ToolStateService struct {
	access toolAccess
	repo   repository.AgentToolRepository
}
type ToolState struct {
	Plan         *model.AgentPlan        `json:"plan"`
	Interactions []model.ToolInteraction `json:"interactions"`
	Artifacts    []model.AgentArtifact   `json:"artifacts"`
}

func NewToolState(repo repository.AgentToolRepository, conversations repository.ConversationRepository) *ToolStateService {
	return &ToolStateService{toolAccess{conversations: conversations}, repo}
}
func (s *ToolStateService) Get(ctx context.Context, userID, conversationID string) (ToolState, error) {
	var state ToolState
	if err := s.access.owned(ctx, userID, conversationID); err != nil {
		return state, err
	}
	var err error
	if state.Plan, err = s.repo.GetPlan(ctx, userID, conversationID); err != nil {
		return state, err
	}
	if state.Interactions, err = s.repo.PendingInteractions(ctx, userID, conversationID); err != nil {
		return state, err
	}
	state.Artifacts, err = s.repo.ListArtifacts(ctx, userID, conversationID)
	return state, err
}
