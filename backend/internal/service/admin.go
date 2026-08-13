package service

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/example/agent-platform/backend/internal/database/model"
	"github.com/example/agent-platform/backend/internal/domain"
	"github.com/example/agent-platform/backend/internal/logger"
	"github.com/example/agent-platform/backend/internal/repository"
)

// AdminService 聚合管理员运营能力：封禁/解封和额度调整。
// 所有操作都会写入账号审计日志，记录执行管理员和目标用户。
type AdminService struct {
	users  repository.UserRepository
	auth   repository.AuthRepository
	quotas *QuotaService
	now    func() time.Time
}

func NewAdmin(users repository.UserRepository, auth repository.AuthRepository, quotas *QuotaService) *AdminService {
	return &AdminService{users: users, auth: auth, quotas: quotas, now: time.Now}
}

// IsAdmin 供鉴权中间件判断当前用户是否具有管理员角色。
func (s *AdminService) IsAdmin(ctx context.Context, userID string) (bool, error) {
	user, err := s.users.FindByID(ctx, userID)
	if errors.Is(err, domain.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return user.Role == model.UserRoleAdmin, nil
}

// Ban 封禁用户并立即撤销其全部设备会话。
func (s *AdminService) Ban(ctx context.Context, adminID, targetID, reason, userAgent string) error {
	if adminID == targetID {
		return ErrAdminSelfOperation
	}
	if err := s.users.SetBanned(ctx, targetID, true, s.now()); err != nil {
		return err
	}
	if err := s.auth.RevokeAllSessions(ctx, targetID, "admin_ban", s.now()); err != nil {
		return err
	}
	s.audit(ctx, targetID, "admin.banned", adminID, userAgent, map[string]any{"reason": reason})
	return nil
}

// Unban 解除封禁。被封禁期间撤销的会话不会恢复，用户需要重新登录。
func (s *AdminService) Unban(ctx context.Context, adminID, targetID, userAgent string) error {
	if err := s.users.SetBanned(ctx, targetID, false, s.now()); err != nil {
		return err
	}
	s.audit(ctx, targetID, "admin.unbanned", adminID, userAgent, nil)
	return nil
}

// SetQuotaLimits 调整目标用户的日/月额度覆盖值。
// 0 表示沿用部署默认额度，负数表示不限量。
func (s *AdminService) SetQuotaLimits(ctx context.Context, adminID, targetID string, daily, monthly int64, userAgent string) (QuotaStatus, error) {
	if daily < model.QuotaOverrideUnlimited || monthly < model.QuotaOverrideUnlimited {
		return QuotaStatus{}, ErrInvalidQuotaLimit
	}
	status, err := s.quotas.SetLimits(ctx, targetID, daily, monthly)
	if err != nil {
		return QuotaStatus{}, err
	}
	s.audit(ctx, targetID, "admin.quota_adjusted", adminID, userAgent, map[string]any{
		"dailyOverride": daily, "monthlyOverride": monthly,
	})
	return status, nil
}

// QuotaStatus 查询任意用户的额度快照。
func (s *AdminService) QuotaStatus(ctx context.Context, targetID string) (QuotaStatus, error) {
	return s.quotas.Status(ctx, targetID)
}

func (s *AdminService) audit(ctx context.Context, targetID, event, adminID, userAgent string, metadata map[string]any) {
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["adminId"] = adminID
	payload, _ := json.Marshal(metadata)
	entry := model.AccountAuditLog{
		UserID:    targetID,
		Event:     event,
		UserAgent: truncate(userAgent, 500),
		Metadata:  payload,
	}
	if err := s.auth.RecordAudit(ctx, &entry); err != nil {
		logger.Error(ctx, "admin audit persistence failed", logger.Err(err), logger.String("event", event), logger.String("target_user_id", targetID))
	}
}
