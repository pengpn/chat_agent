package repository

import (
	"context"
	"errors"
	"time"

	"github.com/example/agent-platform/backend/internal/database/model"
	"github.com/example/agent-platform/backend/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// QuotaLimits 是部署级默认额度。用户行上的覆盖值为 0 时沿用这里的默认值。
type QuotaLimits struct {
	DefaultDailyTokens   int64
	DefaultMonthlyTokens int64
}

// QuotaRepository 持久化额度预扣、结算和补偿。
// 所有写操作都在锁定用户额度行的事务内完成，保证同一用户的并发请求
// 串行判断余额，不会出现两个请求各自看到足够余额后双双超额的情况。
type QuotaRepository interface {
	// Reserve 预扣估算 Token。当前 streaming 消息数量（含本请求自身）
	// 超过 maxConcurrent 时返回 domain.ErrTooManyConcurrent；
	// maxConcurrent <= 0 表示不检查并发。
	Reserve(ctx context.Context, userID string, tokens int64, limits QuotaLimits, maxConcurrent int, now time.Time) error
	// Settle 用实际用量替换预扣：先退回 reserved，再记入 actual。
	Settle(ctx context.Context, userID string, reserved, actual int64, now time.Time) error
	// Release 补偿失败请求：退回预扣的 reserved。
	Release(ctx context.Context, userID string, reserved int64, now time.Time) error
	Status(ctx context.Context, userID string, limits QuotaLimits, now time.Time) (model.UserQuota, error)
	SetLimits(ctx context.Context, userID string, daily, monthly int64, now time.Time) (model.UserQuota, error)
}

type GormQuotaRepository struct{ db *gorm.DB }

func newQuotaRepository(db *gorm.DB) *GormQuotaRepository { return &GormQuotaRepository{db: db} }

func (r *GormQuotaRepository) Reserve(ctx context.Context, userID string, tokens int64, limits QuotaLimits, maxConcurrent int, now time.Time) error {
	if tokens <= 0 {
		return nil
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		quota, err := lockOrCreateQuota(tx, userID, now)
		if err != nil {
			return err
		}
		rollQuotaWindows(quota, now)
		if maxConcurrent > 0 {
			var active int64
			// streaming 消息数即正在生成的请求数，包含当前请求自己创建的
			// streaming 助手消息，因此允许条件是 active <= maxConcurrent。
			if err := tx.Model(&model.Message{}).
				Joins("JOIN conversations ON conversations.id = messages.conversation_id").
				Where("conversations.user_id = ? AND messages.status = ?", userID, "streaming").
				Count(&active).Error; err != nil {
				return err
			}
			if active > int64(maxConcurrent) {
				return domain.ErrTooManyConcurrent
			}
		}
		dailyLimit := effectiveQuotaLimit(quota.DailyTokenLimit, limits.DefaultDailyTokens)
		monthlyLimit := effectiveQuotaLimit(quota.MonthlyTokenLimit, limits.DefaultMonthlyTokens)
		if dailyLimit >= 0 && quota.DailyUsedTokens+tokens > dailyLimit {
			return domain.ErrDailyQuotaExceeded
		}
		if monthlyLimit >= 0 && quota.MonthlyUsedTokens+tokens > monthlyLimit {
			return domain.ErrMonthlyQuotaExceeded
		}
		quota.DailyUsedTokens += tokens
		quota.MonthlyUsedTokens += tokens
		return tx.Save(quota).Error
	})
}

func (r *GormQuotaRepository) Settle(ctx context.Context, userID string, reserved, actual int64, now time.Time) error {
	return r.adjustUsed(ctx, userID, now, func(quota *model.UserQuota) {
		quota.DailyUsedTokens += actual - reserved
		quota.MonthlyUsedTokens += actual - reserved
	})
}

func (r *GormQuotaRepository) Release(ctx context.Context, userID string, reserved int64, now time.Time) error {
	return r.adjustUsed(ctx, userID, now, func(quota *model.UserQuota) {
		quota.DailyUsedTokens -= reserved
		quota.MonthlyUsedTokens -= reserved
	})
}

func (r *GormQuotaRepository) adjustUsed(ctx context.Context, userID string, now time.Time, adjust func(*model.UserQuota)) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		quota, err := lockOrCreateQuota(tx, userID, now)
		if err != nil {
			return err
		}
		rollQuotaWindows(quota, now)
		adjust(quota)
		// 跨窗口结算或重复补偿可能把用量推成负数，统一钳制到 0。
		if quota.DailyUsedTokens < 0 {
			quota.DailyUsedTokens = 0
		}
		if quota.MonthlyUsedTokens < 0 {
			quota.MonthlyUsedTokens = 0
		}
		return tx.Save(quota).Error
	})
}

func (r *GormQuotaRepository) Status(ctx context.Context, userID string, limits QuotaLimits, now time.Time) (model.UserQuota, error) {
	var result model.UserQuota
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		quota, err := lockOrCreateQuota(tx, userID, now)
		if err != nil {
			return err
		}
		rollQuotaWindows(quota, now)
		if err := tx.Save(quota).Error; err != nil {
			return err
		}
		result = *quota
		return nil
	})
	return result, err
}

func (r *GormQuotaRepository) SetLimits(ctx context.Context, userID string, daily, monthly int64, now time.Time) (model.UserQuota, error) {
	var result model.UserQuota
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		quota, err := lockOrCreateQuota(tx, userID, now)
		if err != nil {
			return err
		}
		quota.DailyTokenLimit = daily
		quota.MonthlyTokenLimit = monthly
		if err := tx.Save(quota).Error; err != nil {
			return err
		}
		result = *quota
		return nil
	})
	return result, err
}

// lockOrCreateQuota 以行锁读取用户额度行；首次访问时创建零值行。
// 行锁把同一用户的额度判断串行化，是预扣一致性的基础。
func lockOrCreateQuota(tx *gorm.DB, userID string, now time.Time) (*model.UserQuota, error) {
	var quota model.UserQuota
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ?", userID).First(&quota).Error
	if err == nil {
		return &quota, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	quota = model.UserQuota{
		UserID:            userID,
		DailyWindowDate:   dailyWindow(now),
		MonthlyWindowDate: monthlyWindow(now),
	}
	if err := tx.Create(&quota).Error; err != nil {
		if isUniqueViolation(err) {
			// 并发首次访问时另一个事务已经创建，重新加锁读取。
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ?", userID).First(&quota).Error; err != nil {
				return nil, err
			}
			return &quota, nil
		}
		return nil, err
	}
	return &quota, nil
}

// rollQuotaWindows 在读取时惰性滚动窗口：跨自然日或自然月后用量归零。
func rollQuotaWindows(quota *model.UserQuota, now time.Time) {
	if day := dailyWindow(now); quota.DailyWindowDate != day {
		quota.DailyWindowDate = day
		quota.DailyUsedTokens = 0
	}
	if month := monthlyWindow(now); quota.MonthlyWindowDate != month {
		quota.MonthlyWindowDate = month
		quota.MonthlyUsedTokens = 0
	}
}

// effectiveQuotaLimit 合并管理员覆盖值与部署默认值：
// 覆盖值大于 0 时生效，等于 0 沿用默认，小于 0 表示不限量（返回 -1）。
func effectiveQuotaLimit(override, fallback int64) int64 {
	if override > 0 {
		return override
	}
	if override < 0 {
		return -1
	}
	return fallback
}

func dailyWindow(now time.Time) string   { return now.Format("2006-01-02") }
func monthlyWindow(now time.Time) string { return now.Format("2006-01") }
