package model

import "time"

// Conversation is the persisted conversation model.
type Conversation struct {
	ID        string    `json:"id" gorm:"type:uuid;default:gen_random_uuid();primaryKey"`
	UserID    string    `json:"-" gorm:"type:uuid;not null;index:conversations_user_updated_idx,priority:1"`
	User      *User     `json:"-" gorm:"foreignKey:UserID;references:ID;constraint:OnDelete:CASCADE"`
	Title     string    `json:"title" gorm:"type:text;not null;default:New conversation"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt" gorm:"index:conversations_user_updated_idx,priority:2,sort:desc"`
}
