package service

import (
	"context"
	"errors"

	"github.com/example/agent-platform/backend/internal/domain"
	"github.com/example/agent-platform/backend/internal/execution"
	"github.com/example/agent-platform/backend/internal/repository"
	"github.com/example/agent-platform/backend/internal/userevent"
	"github.com/google/uuid"
)

var ErrInvalidToolInput = errors.New("工具输入或用户答复无效")

// toolAccess 只复用会话所有权和状态通知，不承载工具分派或各业务规则。
type toolAccess struct {
	conversations repository.ConversationRepository
	events        *userevent.Hub
}

func (a toolAccess) owned(ctx context.Context, userID, conversationID string) error {
	if _, err := uuid.Parse(conversationID); err != nil {
		return domain.ErrNotFound
	}
	_, err := a.conversations.FindByIDForUser(ctx, userID, conversationID)
	return err
}
func (a toolAccess) scope(ctx context.Context) (execution.Scope, error) {
	scope, err := execution.ScopeFromContext(ctx)
	if err != nil {
		return scope, err
	}
	return scope, a.owned(ctx, scope.UserID, scope.ConversationID)
}
func (a toolAccess) changed(userID, conversationID string) {
	a.events.Publish(userID, "agent.state.changed", map[string]any{"conversationId": conversationID})
}
