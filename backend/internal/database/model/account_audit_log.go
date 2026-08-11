package model

import (
	"encoding/json"
	"time"
)

// AccountAuditLog intentionally has no foreign key to users. Security events
// can therefore remain for the configured audit retention window after an
// account is deleted, without retaining email or password data.
type AccountAuditLog struct {
	ID        string          `json:"id" gorm:"type:uuid;default:gen_random_uuid();primaryKey"`
	UserID    string          `json:"userId" gorm:"type:uuid;not null;index"`
	SessionID *string         `json:"sessionId,omitempty" gorm:"type:uuid;index"`
	Event     string          `json:"event" gorm:"type:varchar(80);not null;index"`
	UserAgent string          `json:"-" gorm:"type:text;not null"`
	Metadata  json.RawMessage `json:"metadata" gorm:"type:jsonb;not null;default:'{}'"`
	CreatedAt time.Time       `json:"createdAt" gorm:"index"`
}
