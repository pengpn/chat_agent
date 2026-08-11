package model

import "time"

// AuthSession represents one signed-in browser/device. The raw refresh token
// never reaches PostgreSQL; rotation replaces RefreshTokenHash atomically.
type AuthSession struct {
	ID               string     `json:"id" gorm:"type:uuid;primaryKey"`
	UserID           string     `json:"-" gorm:"type:uuid;not null;index"`
	User             *User      `json:"-" gorm:"foreignKey:UserID;references:ID;constraint:OnDelete:CASCADE"`
	RefreshTokenHash string     `json:"-" gorm:"type:char(64);not null;uniqueIndex"`
	DeviceName       string     `json:"deviceName" gorm:"type:varchar(120);not null"`
	UserAgent        string     `json:"-" gorm:"type:text;not null"`
	ExpiresAt        time.Time  `json:"expiresAt" gorm:"not null;index"`
	LastUsedAt       time.Time  `json:"lastUsedAt" gorm:"not null"`
	RevokedAt        *time.Time `json:"revokedAt,omitempty" gorm:"index"`
	RevokedReason    string     `json:"-" gorm:"type:varchar(80);not null;default:''"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"-"`
}
