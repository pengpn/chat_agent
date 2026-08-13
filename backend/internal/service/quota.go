package service

import (
	"context"
	"errors"
	"time"

	"github.com/example/agent-platform/backend/internal/database/model"
	"github.com/example/agent-platform/backend/internal/domain"
	"github.com/example/agent-platform/backend/internal/repository"
)

// QuotaConfig 描述部署级额度策略。Enabled 为 false 时 QuotaService 是
// 无副作用的空实现，所有准入请求直接放行。
type QuotaConfig struct {
	Enabled bool
	// DefaultDailyTokens 与 DefaultMonthlyTokens 是未被管理员覆盖的用户
	// 沿用的默认额度。
	DefaultDailyTokens   int64
	DefaultMonthlyTokens int64
	// MaxConcurrentGenerations 是单用户同时处于生成中的请求上限。
	MaxConcurrentGenerations int
}

// QuotaService 负责额度的预扣、结算和补偿。它与 Rate Limit 的分工：
// Rate Limit 限制单位时间内的请求次数，防止瞬时洪峰；
// Quota 限制累计 Token 用量，控制成本；
// 并发限制控制同时在飞的生成请求数量，防止单用户占满服务能力。
type QuotaService struct {
	repository repository.QuotaRepository
	config     QuotaConfig
	now        func() time.Time
}

func NewQuota(repo repository.QuotaRepository, config QuotaConfig) *QuotaService {
	return &QuotaService{repository: repo, config: config, now: time.Now}
}

// QuotaStatus 是对外展示的额度快照，额度限制已经合并了管理员覆盖值。
type QuotaStatus struct {
	DailyLimit      int64 `json:"dailyLimit"`
	DailyUsed       int64 `json:"dailyUsed"`
	MonthlyLimit    int64 `json:"monthlyLimit"`
	MonthlyUsed     int64 `json:"monthlyUsed"`
	DailyOverride   int64 `json:"dailyOverride"`
	MonthlyOverride int64 `json:"monthlyOverride"`
	MaxConcurrent   int   `json:"maxConcurrent"`
}

// Reserve 在调用模型前预扣估算 Token。调用方必须在请求结束时调用
// Settle 或 Release，否则预扣部分会一直占用额度，直到窗口滚动。
func (s *QuotaService) Reserve(ctx context.Context, userID string, estimatedTokens int64) error {
	if s == nil || !s.config.Enabled || estimatedTokens <= 0 {
		return nil
	}
	err := s.repository.Reserve(ctx, userID, estimatedTokens, s.limits(), s.config.MaxConcurrentGenerations, s.now())
	switch {
	case errors.Is(err, domain.ErrDailyQuotaExceeded):
		return ErrDailyQuotaExceeded
	case errors.Is(err, domain.ErrMonthlyQuotaExceeded):
		return ErrMonthlyQuotaExceeded
	case errors.Is(err, domain.ErrTooManyConcurrent):
		return ErrTooManyConcurrentGenerations
	}
	return err
}

// Settle 按实际用量结算：退回预扣部分并记入实际用量。
// 实际用量未知时调用方应传入预扣值，宁可多扣不漏扣。
func (s *QuotaService) Settle(ctx context.Context, userID string, reserved, actual int64) error {
	if s == nil || !s.config.Enabled || reserved <= 0 {
		return nil
	}
	return s.repository.Settle(ctx, userID, reserved, actual, s.now())
}

// Release 补偿失败请求，退回全部预扣额度。
func (s *QuotaService) Release(ctx context.Context, userID string, reserved int64) error {
	if s == nil || !s.config.Enabled || reserved <= 0 {
		return nil
	}
	return s.repository.Release(ctx, userID, reserved, s.now())
}

func (s *QuotaService) Status(ctx context.Context, userID string) (QuotaStatus, error) {
	if s == nil || !s.config.Enabled {
		return QuotaStatus{MaxConcurrent: 0}, nil
	}
	quota, err := s.repository.Status(ctx, userID, s.limits(), s.now())
	if err != nil {
		return QuotaStatus{}, err
	}
	return quotaStatus(quota, s.config), nil
}

// SetLimits 保存管理员额度覆盖值。0 表示沿用默认，负数表示不限量。
func (s *QuotaService) SetLimits(ctx context.Context, userID string, daily, monthly int64) (QuotaStatus, error) {
	if s == nil || !s.config.Enabled {
		return QuotaStatus{}, nil
	}
	quota, err := s.repository.SetLimits(ctx, userID, daily, monthly, s.now())
	if err != nil {
		return QuotaStatus{}, err
	}
	return quotaStatus(quota, s.config), nil
}

func (s *QuotaService) limits() repository.QuotaLimits {
	return repository.QuotaLimits{
		DefaultDailyTokens:   s.config.DefaultDailyTokens,
		DefaultMonthlyTokens: s.config.DefaultMonthlyTokens,
	}
}

func quotaStatus(quota model.UserQuota, config QuotaConfig) QuotaStatus {
	return QuotaStatus{
		DailyLimit:      effectiveLimitForDisplay(quota.DailyTokenLimit, config.DefaultDailyTokens),
		DailyUsed:       quota.DailyUsedTokens,
		MonthlyLimit:    effectiveLimitForDisplay(quota.MonthlyTokenLimit, config.DefaultMonthlyTokens),
		MonthlyUsed:     quota.MonthlyUsedTokens,
		DailyOverride:   quota.DailyTokenLimit,
		MonthlyOverride: quota.MonthlyTokenLimit,
		MaxConcurrent:   config.MaxConcurrentGenerations,
	}
}

// effectiveLimitForDisplay 与仓储层的合并规则一致；-1 表示不限量。
func effectiveLimitForDisplay(override, fallback int64) int64 {
	if override > 0 {
		return override
	}
	if override < 0 {
		return -1
	}
	return fallback
}
