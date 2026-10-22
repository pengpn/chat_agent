package repository

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/example/agent-platform/backend/internal/database/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrInteractionClosed = errors.New("交互已处理或已过期，请刷新状态")

// AgentToolRepository 集中计划、用户交互和产物的持久化，所有查询都带用户和会话范围。
type AgentToolRepository interface {
	SavePlan(ctx context.Context, plan *model.AgentPlan) error
	GetPlan(ctx context.Context, userID, conversationID string) (*model.AgentPlan, error)
	CreateInteraction(ctx context.Context, interaction *model.ToolInteraction) error
	GetInteraction(ctx context.Context, userID, conversationID, interactionID string) (model.ToolInteraction, error)
	PendingInteractions(ctx context.Context, userID, conversationID string) ([]model.ToolInteraction, error)
	Respond(ctx context.Context, userID, conversationID, interactionID string, response json.RawMessage) error
	CancelInteraction(ctx context.Context, userID, conversationID, interactionID string) error
	ConsumeApproval(ctx context.Context, userID, conversationID, operationID, approvalKey string) (bool, error)
	SaveArtifact(ctx context.Context, artifact *model.AgentArtifact) error
	GetArtifact(ctx context.Context, userID, conversationID, artifactID string) (model.AgentArtifact, error)
	ListArtifacts(ctx context.Context, userID, conversationID string) ([]model.AgentArtifact, error)
}
type agentToolRepository struct{ db *gorm.DB }

func newAgentToolRepository(db *gorm.DB) AgentToolRepository { return &agentToolRepository{db} }

func (r *agentToolRepository) SavePlan(ctx context.Context, p *model.AgentPlan) error {
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "conversation_id"}}, DoUpdates: clause.Assignments(map[string]any{"steps": p.Steps, "revision": gorm.Expr("agent_plans.revision + 1"), "updated_at": time.Now()})}).Create(p).Error
}
func (r *agentToolRepository) GetPlan(ctx context.Context, u, c string) (*model.AgentPlan, error) {
	var p model.AgentPlan
	err := r.db.WithContext(ctx).Where("user_id = ? AND conversation_id = ?", u, c).First(&p).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &p, err
}
func (r *agentToolRepository) CreateInteraction(ctx context.Context, i *model.ToolInteraction) error {
	return r.db.WithContext(ctx).Create(i).Error
}
func (r *agentToolRepository) GetInteraction(ctx context.Context, u, c, id string) (model.ToolInteraction, error) {
	var item model.ToolInteraction
	err := r.db.WithContext(ctx).Where("id = ? AND user_id = ? AND conversation_id = ?", id, u, c).First(&item).Error
	return item, err
}
func (r *agentToolRepository) PendingInteractions(ctx context.Context, u, c string) ([]model.ToolInteraction, error) {
	items := []model.ToolInteraction{}
	err := r.db.WithContext(ctx).Where("user_id = ? AND conversation_id = ? AND status = 'pending' AND expires_at > ?", u, c, time.Now()).Order("created_at ASC").Limit(20).Find(&items).Error
	return items, err
}
func (r *agentToolRepository) Respond(ctx context.Context, u, c, id string, response json.RawMessage) error {
	result := r.db.WithContext(ctx).Model(&model.ToolInteraction{}).Where("id = ? AND user_id = ? AND conversation_id = ? AND status = 'pending' AND expires_at > ?", id, u, c, time.Now()).Updates(map[string]any{"status": "responded", "response": response})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrInteractionClosed
	}
	return nil
}
func (r *agentToolRepository) CancelInteraction(ctx context.Context, u, c, id string) error {
	return r.db.WithContext(ctx).Model(&model.ToolInteraction{}).Where("id = ? AND user_id = ? AND conversation_id = ? AND status IN ('pending','responded')", id, u, c).Update("status", "canceled").Error
}
func (r *agentToolRepository) ConsumeApproval(ctx context.Context, u, c, operation, key string) (bool, error) {
	// 事务锁定单张匹配票据；并发执行也只能消费一次，拒绝/提问不能充当授权。
	var consumed bool
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var item model.ToolInteraction
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ? AND conversation_id = ? AND operation_id = ? AND approval_key = ? AND kind = 'approval' AND status = 'responded' AND expires_at > ? AND response->>'approved' = 'true'", u, c, operation, key, time.Now()).Order("created_at ASC").First(&item).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := tx.Model(&item).Update("status", "consumed").Error; err != nil {
			return err
		}
		consumed = true
		return nil
	})
	return consumed, err
}
func (r *agentToolRepository) SaveArtifact(ctx context.Context, a *model.AgentArtifact) error {
	return r.db.WithContext(ctx).Create(a).Error
}
func (r *agentToolRepository) GetArtifact(ctx context.Context, u, c, id string) (model.AgentArtifact, error) {
	var a model.AgentArtifact
	err := r.db.WithContext(ctx).Where("id = ? AND user_id = ? AND conversation_id = ?", id, u, c).First(&a).Error
	return a, err
}
func (r *agentToolRepository) ListArtifacts(ctx context.Context, u, c string) ([]model.AgentArtifact, error) {
	items := []model.AgentArtifact{}
	err := r.db.WithContext(ctx).Omit("content").Where("user_id = ? AND conversation_id = ?", u, c).Order("created_at DESC, id DESC").Limit(50).Find(&items).Error
	return items, err
}
