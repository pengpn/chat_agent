package model

import "time"

// User is the persisted account model exposed by the current API.
type User struct {
	ID                string     `json:"id" gorm:"type:uuid;default:gen_random_uuid();primaryKey"`
	Email             string     `json:"email" gorm:"type:text;not null;unique"`
	PasswordHash      string     `json:"-" gorm:"type:text;not null"`
	DisplayName       string     `json:"displayName" gorm:"type:text;not null"`
	EmailVerifiedAt   *time.Time `json:"emailVerifiedAt,omitempty"`
	PasswordChangedAt *time.Time `json:"-"`
	CreatedAt         time.Time  `json:"createdAt"`
	UpdatedAt         time.Time  `json:"-"`
}
