package model

import "time"

// 角色常量：Role 字段目前只区分普通用户和管理员。
const (
	UserRoleUser  = "user"
	UserRoleAdmin = "admin"
)

// User is the persisted account model exposed by the current API.
type User struct {
	ID                string     `json:"id" gorm:"type:uuid;default:gen_random_uuid();primaryKey"`
	Email             string     `json:"email" gorm:"type:text;not null;unique"`
	PasswordHash      string     `json:"-" gorm:"type:text;not null"`
	DisplayName       string     `json:"displayName" gorm:"type:text;not null"`
	Role              string     `json:"role" gorm:"type:varchar(20);not null;default:'user'"`
	BannedAt          *time.Time `json:"bannedAt,omitempty"`
	EmailVerifiedAt   *time.Time `json:"emailVerifiedAt,omitempty"`
	PasswordChangedAt *time.Time `json:"-"`
	CreatedAt         time.Time  `json:"createdAt"`
	UpdatedAt         time.Time  `json:"-"`
}
