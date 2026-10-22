package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/example/agent-platform/backend/internal/database/model"
	"github.com/example/agent-platform/backend/internal/domain"
	"github.com/example/agent-platform/backend/internal/repository"
	"github.com/example/agent-platform/backend/internal/userevent"
)

// PlanService 只处理任务计划，不接收工具名或原始工具 JSON。
type PlanService struct {
	access toolAccess
	repo   repository.AgentToolRepository
}

func NewPlan(repo repository.AgentToolRepository, conversations repository.ConversationRepository, events *userevent.Hub) *PlanService {
	return &PlanService{toolAccess{conversations, events}, repo}
}
func (s *PlanService) Update(ctx context.Context, input domain.UpdatePlanInput) (domain.Plan, error) {
	var result domain.Plan
	scope, err := s.access.scope(ctx)
	if err != nil {
		return result, err
	}
	if len(input.Steps) == 0 || len(input.Steps) > 20 {
		return result, ErrInvalidToolInput
	}
	active := 0
	for _, step := range input.Steps {
		if strings.TrimSpace(step.Title) == "" || len([]rune(step.Title)) > 200 {
			return result, ErrInvalidToolInput
		}
		switch step.Status {
		case "in_progress":
			active++
		case "pending", "completed":
		default:
			return result, ErrInvalidToolInput
		}
	}
	if active > 1 {
		return result, fmt.Errorf("计划最多有一个进行中的步骤")
	}
	raw, err := json.Marshal(input.Steps)
	if err != nil {
		return result, err
	}
	plan := model.AgentPlan{UserID: scope.UserID, ConversationID: scope.ConversationID, Steps: raw, Revision: 1}
	if err := s.repo.SavePlan(ctx, &plan); err != nil {
		return result, err
	}
	s.access.changed(scope.UserID, scope.ConversationID)
	stored, err := s.repo.GetPlan(ctx, scope.UserID, scope.ConversationID)
	if err != nil {
		return result, err
	}
	if stored == nil {
		return result, fmt.Errorf("计划保存后未找到记录")
	}
	result.ConversationID = stored.ConversationID
	result.Revision = stored.Revision
	result.UpdatedAt = stored.UpdatedAt
	err = json.Unmarshal(stored.Steps, &result.Steps)
	return result, err
}
