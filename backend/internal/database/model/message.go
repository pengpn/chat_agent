package model

import "time"

// Message is the persisted message model.
type Message struct {
	ID             string        `json:"id" gorm:"type:uuid;default:gen_random_uuid();primaryKey"`
	ConversationID string        `json:"conversationId" gorm:"type:uuid;not null;index:messages_conversation_created_idx,priority:1"`
	Conversation   *Conversation `json:"-" gorm:"foreignKey:ConversationID;references:ID;constraint:OnDelete:CASCADE"`
	Role           string        `json:"role" gorm:"type:text;not null;check:messages_role_check,role IN ('system', 'user', 'assistant', 'tool')"`
	Content        string        `json:"content" gorm:"type:text;not null;default:''"`
	Provider       string        `json:"provider,omitempty" gorm:"type:text"`
	Model          string        `json:"model,omitempty" gorm:"type:text"`
	Status         string        `json:"status" gorm:"type:text;not null;default:complete;check:messages_status_check,status IN ('streaming', 'complete', 'stopped', 'error')"`
	CreatedAt      time.Time     `json:"createdAt" gorm:"index:messages_conversation_created_idx,priority:2"`
	UpdatedAt      time.Time     `json:"-"`
}
