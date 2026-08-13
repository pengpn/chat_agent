package model

import "time"

// QuotaOverrideUnlimited 是管理员额度覆盖值的特殊取值：
// 负数表示该用户不受对应方向的额度限制。
const QuotaOverrideUnlimited int64 = -1

// UserQuota 持久化单个用户的 Token 额度与用量。
// 预扣-结算模型：请求前按估算 Token 预扣（Reserve），请求结束后按实际
// 用量结算（Settle）或在失败时补偿（Release）。DailyWindowDate 和
// MonthlyWindowDate 记录当前用量所属的自然日/自然月，跨窗口时用量归零，
// 因此不需要后台定时任务重置。
type UserQuota struct {
	UserID string `json:"userId" gorm:"type:uuid;primaryKey"`
	// DailyTokenLimit 与 MonthlyTokenLimit 是管理员覆盖值：
	// 0 表示沿用部署默认额度，负数表示不限量，正数为显式额度。
	DailyTokenLimit   int64 `json:"dailyTokenLimit" gorm:"not null;default:0"`
	MonthlyTokenLimit int64 `json:"monthlyTokenLimit" gorm:"not null;default:0"`
	// 已用量包含尚未结算的预扣部分，保证并发预扣不会超额。
	DailyUsedTokens   int64 `json:"dailyUsedTokens" gorm:"not null;default:0"`
	MonthlyUsedTokens int64 `json:"monthlyUsedTokens" gorm:"not null;default:0"`
	// 窗口标记使用本地时区的日期字符串，例如 2026-08-13 和 2026-08。
	DailyWindowDate   string    `json:"dailyWindowDate" gorm:"type:varchar(10);not null"`
	MonthlyWindowDate string    `json:"monthlyWindowDate" gorm:"type:varchar(7);not null"`
	UpdatedAt         time.Time `json:"updatedAt"`
}
