package model

import (
	"encoding/json"
	"time"
)

// AgentPlan 保存当前会话的任务计划；计划是进度说明，不等于工具执行授权。
type AgentPlan struct {
	ConversationID string          `json:"conversationId" gorm:"type:uuid;primaryKey"`
	Conversation   *Conversation   `json:"-" gorm:"foreignKey:ConversationID;constraint:OnDelete:CASCADE"`
	UserID         string          `json:"-" gorm:"type:uuid;not null;index"`
	Steps          json.RawMessage `json:"steps" gorm:"type:jsonb;not null"`
	Revision       int64           `json:"revision" gorm:"not null;default:1"`
	UpdatedAt      time.Time       `json:"updatedAt"`
}

// ToolInteraction 只有鉴权后的用户响应接口可以把 pending 改为 responded。
// 审批同时固定任务、版本、Schema 与完整参数摘要，消费后不可再次执行。
type ToolInteraction struct {
	ID             string          `json:"id" gorm:"type:uuid;default:gen_random_uuid();primaryKey"`
	ConversationID string          `json:"conversationId" gorm:"type:uuid;not null;index"`
	Conversation   *Conversation   `json:"-" gorm:"foreignKey:ConversationID;constraint:OnDelete:CASCADE"`
	UserID         string          `json:"-" gorm:"type:uuid;not null;index"`
	OperationID    string          `json:"-" gorm:"type:text;not null;index"`
	Kind           string          `json:"kind" gorm:"type:text;not null"`
	Status         string          `json:"status" gorm:"type:text;not null;default:pending;index"`
	Request        json.RawMessage `json:"request" gorm:"type:jsonb;not null"`
	Response       json.RawMessage `json:"-" gorm:"type:jsonb"`
	ApprovalKey    string          `json:"-" gorm:"type:text;not null;default:'';index"`
	ExpiresAt      time.Time       `json:"expiresAt" gorm:"not null;index"`
	CreatedAt      time.Time       `json:"createdAt"`
	UpdatedAt      time.Time       `json:"-"`
}

// AgentArtifact 是不可变的文本产物，每次保存创建新 ID，不覆盖之前的内容。
type AgentArtifact struct {
	ID             string        `json:"id" gorm:"type:uuid;default:gen_random_uuid();primaryKey"`
	ConversationID string        `json:"conversationId" gorm:"type:uuid;not null;index"`
	Conversation   *Conversation `json:"-" gorm:"foreignKey:ConversationID;constraint:OnDelete:CASCADE"`
	UserID         string        `json:"-" gorm:"type:uuid;not null;index"`
	Title          string        `json:"title" gorm:"type:text;not null"`
	MediaType      string        `json:"mediaType" gorm:"type:text;not null"`
	Content        string        `json:"content,omitempty" gorm:"type:text;not null"`
	SHA256         string        `json:"sha256" gorm:"type:text;not null"`
	Size           int           `json:"size" gorm:"not null"`
	CreatedAt      time.Time     `json:"createdAt"`
}
