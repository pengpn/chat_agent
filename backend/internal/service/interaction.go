package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/example/agent-platform/backend/internal/database/model"
	"github.com/example/agent-platform/backend/internal/domain"
	"github.com/example/agent-platform/backend/internal/execution"
	"github.com/example/agent-platform/backend/internal/repository"
	"github.com/example/agent-platform/backend/internal/userevent"
	"github.com/google/uuid"
)

// InteractionService 负责真实用户答复、审批票据和等待/取消状态流转。
type InteractionService struct {
	access  toolAccess
	repo    repository.AgentToolRepository
	timeout time.Duration
}

func NewInteraction(repo repository.AgentToolRepository, conversations repository.ConversationRepository, events *userevent.Hub, timeout time.Duration) *InteractionService {
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	return &InteractionService{toolAccess{conversations, events}, repo, timeout}
}

type ToolUserResponse struct {
	Answer   string `json:"answer,omitempty"`
	Approved *bool  `json:"approved,omitempty"`
}

func (s *InteractionService) Respond(ctx context.Context, userID, conversationID, id string, response ToolUserResponse) error {
	if err := s.access.owned(ctx, userID, conversationID); err != nil {
		return err
	}
	if _, err := uuid.Parse(id); err != nil {
		return domain.ErrNotFound
	}
	item, err := s.repo.GetInteraction(ctx, userID, conversationID, id)
	if err != nil {
		return err
	}
	response.Answer = strings.TrimSpace(response.Answer)
	switch item.Kind {
	case "question":
		if response.Answer == "" || len(response.Answer) > 8000 || response.Approved != nil {
			return ErrInvalidToolInput
		}
	case "approval":
		if response.Approved == nil || response.Answer != "" {
			return ErrInvalidToolInput
		}
	default:
		return ErrInvalidToolInput
	}
	raw, err := json.Marshal(response)
	if err != nil {
		return err
	}
	if err := s.repo.Respond(ctx, userID, conversationID, id, raw); err != nil {
		return err
	}
	s.access.changed(userID, conversationID)
	return nil
}
func (s *InteractionService) Ask(ctx context.Context, input domain.AskUserInput) (domain.UserAnswer, error) {
	var result domain.UserAnswer
	if strings.TrimSpace(input.Question) == "" || len([]rune(input.Question)) > 1000 || (len(input.Options) > 0 && (len(input.Options) < 2 || len(input.Options) > 5)) {
		return result, ErrInvalidToolInput
	}
	for _, option := range input.Options {
		if strings.TrimSpace(option) == "" || len([]rune(option)) > 200 {
			return result, ErrInvalidToolInput
		}
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return result, err
	}
	item, err := s.newInteraction(ctx, "question", raw)
	if err != nil {
		return result, err
	}
	response, err := s.wait(ctx, item)
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal(response, &result); err != nil {
		return result, err
	}
	if result.Answer == "" {
		return result, ErrInvalidToolInput
	}
	return result, nil
}
func (s *InteractionService) RequestApproval(ctx context.Context, input domain.ApprovalRequest) (domain.ApprovalAnswer, error) {
	var result domain.ApprovalAnswer
	if !input.Target.RequiresApproval || strings.TrimSpace(input.Reason) == "" || len([]rune(input.Reason)) > 1000 {
		return result, ErrInvalidToolInput
	}
	key, err := ApprovalKey(input.Target, string(input.Arguments))
	if err != nil {
		return result, err
	}
	raw, err := json.Marshal(map[string]any{"tool": input.Target.WireName, "arguments": input.Arguments, "reason": input.Reason, "descriptor": input.Target})
	if err != nil {
		return result, err
	}
	item, err := s.newInteraction(ctx, "approval", raw)
	if err != nil {
		return result, err
	}
	item.ApprovalKey = key
	response, err := s.wait(ctx, item)
	if err != nil {
		return result, err
	}
	var userResponse ToolUserResponse
	if err := json.Unmarshal(response, &userResponse); err != nil {
		return result, err
	}
	if userResponse.Approved == nil {
		return result, ErrInvalidToolInput
	}
	return domain.ApprovalAnswer{Approved: *userResponse.Approved}, nil
}
func (s *InteractionService) newInteraction(ctx context.Context, kind string, request json.RawMessage) (model.ToolInteraction, error) {
	scope, err := s.access.scope(ctx)
	if err != nil {
		return model.ToolInteraction{}, err
	}
	if scope.OperationID == "" {
		return model.ToolInteraction{}, fmt.Errorf("用户交互缺少服务端任务身份")
	}
	item := model.ToolInteraction{UserID: scope.UserID, ConversationID: scope.ConversationID, OperationID: scope.OperationID, Kind: kind, Status: "pending", Request: request, ExpiresAt: time.Now().Add(s.timeout)}
	if deadline, ok := ctx.Deadline(); ok && deadline.Before(item.ExpiresAt) {
		item.ExpiresAt = deadline
	}
	return item, nil
}
func (s *InteractionService) wait(ctx context.Context, item model.ToolInteraction) (json.RawMessage, error) {
	if err := s.repo.CreateInteraction(ctx, &item); err != nil {
		return nil, err
	}
	s.access.changed(item.UserID, item.ConversationID)
	succeeded := false
	defer func() {
		if !succeeded {
			cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = s.repo.CancelInteraction(cleanup, item.UserID, item.ConversationID, item.ID)
		}
		s.access.changed(item.UserID, item.ConversationID)
	}()
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
		if time.Now().After(item.ExpiresAt) {
			return nil, fmt.Errorf("等待用户响应超时")
		}
		latest, err := s.repo.GetInteraction(ctx, item.UserID, item.ConversationID, item.ID)
		if err != nil {
			return nil, err
		}
		if latest.Status == "responded" {
			succeeded = true
			return latest.Response, nil
		}
		if latest.Status != "pending" {
			return nil, repository.ErrInteractionClosed
		}
	}
}

// Authorize 是 Registry 注入的审批门禁，不执行目标工具，也不放宽沙箱权限。
func (s *InteractionService) Authorize(ctx context.Context, target domain.ToolDescriptor, args string) error {
	scope, err := execution.ScopeFromContext(ctx)
	if err != nil {
		return err
	}
	if scope.OperationID == "" {
		return fmt.Errorf("审批缺少服务端任务身份")
	}
	key, err := ApprovalKey(target, args)
	if err != nil {
		return err
	}
	allowed, err := s.repo.ConsumeApproval(ctx, scope.UserID, scope.ConversationID, scope.OperationID, key)
	if err != nil {
		return err
	}
	if !allowed {
		return fmt.Errorf("尚未获得本轮任务的有效一次性审批，请先调用 user_request_approval 并等待用户批准")
	}
	return nil
}
