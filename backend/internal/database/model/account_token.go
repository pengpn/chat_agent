package model

import "time"

const (
	AccountTokenEmailVerification = "email_verification"
	AccountTokenPasswordReset     = "password_reset"
)

// AccountToken is a one-time, expiring credential. TokenHash is the SHA-256
// hash of the value sent by email; UsedAt makes consumption auditable.
type AccountToken struct {
	ID        string     `json:"-" gorm:"type:uuid;default:gen_random_uuid();primaryKey"`
	UserID    string     `json:"-" gorm:"type:uuid;not null;index"`
	User      *User      `json:"-" gorm:"foreignKey:UserID;references:ID;constraint:OnDelete:CASCADE"`
	Purpose   string     `json:"-" gorm:"type:varchar(40);not null;index;check:account_tokens_purpose_check,purpose IN ('email_verification','password_reset')"`
	TokenHash string     `json:"-" gorm:"type:char(64);not null;uniqueIndex"`
	ExpiresAt time.Time  `json:"-" gorm:"not null;index"`
	UsedAt    *time.Time `json:"-"`
	CreatedAt time.Time  `json:"-"`
}
