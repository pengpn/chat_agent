// Package domain contains business concepts shared across application layers.
package domain

import "errors"

var (
	ErrNotFound      = errors.New("domain record not found")
	ErrAlreadyExists = errors.New("domain record already exists")
	ErrInvalid       = errors.New("invalid domain state")
	// ErrDailyQuotaExceeded 表示当日 Token 额度不足。
	ErrDailyQuotaExceeded = errors.New("daily quota exceeded")
	// ErrMonthlyQuotaExceeded 表示当月 Token 额度不足。
	ErrMonthlyQuotaExceeded = errors.New("monthly quota exceeded")
	// ErrTooManyConcurrent 表示超过单用户并发生成上限。
	ErrTooManyConcurrent = errors.New("too many concurrent generations")
)
