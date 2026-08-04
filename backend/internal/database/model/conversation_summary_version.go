package model

import "time"

// ConversationSummaryVersion is an immutable audit record for every generated
// summary candidate. Only an accepted version is copied onto Conversation as
// the active rolling summary.
type ConversationSummaryVersion struct {
	ID                      string        `json:"id" gorm:"type:uuid;default:gen_random_uuid();primaryKey"`
	ConversationID          string        `json:"conversationId" gorm:"type:uuid;not null;uniqueIndex:summary_versions_conversation_version_idx,priority:1;index:summary_versions_conversation_created_idx,priority:1"`
	Conversation            *Conversation `json:"-" gorm:"foreignKey:ConversationID;references:ID;constraint:OnDelete:CASCADE"`
	Version                 int64         `json:"version" gorm:"not null;uniqueIndex:summary_versions_conversation_version_idx,priority:2"`
	Summary                 string        `json:"summary" gorm:"type:text;not null;default:''"`
	ThroughMessageID        string        `json:"throughMessageId" gorm:"type:uuid;not null"`
	Provider                string        `json:"provider" gorm:"type:text;not null"`
	Model                   string        `json:"model" gorm:"type:text;not null"`
	PromptVersion           string        `json:"promptVersion" gorm:"type:text;not null"`
	EvaluationPromptVersion string        `json:"evaluationPromptVersion,omitempty" gorm:"type:text;not null;default:''"`
	Status                  string        `json:"status" gorm:"type:text;not null;check:summary_versions_status_check,status IN ('accepted', 'rejected', 'evaluation_failed')"`
	QualityEvaluated        bool          `json:"qualityEvaluated" gorm:"not null;default:false"`
	QualityPassed           bool          `json:"qualityPassed" gorm:"not null;default:false"`
	QualityScore            int           `json:"qualityScore" gorm:"not null;default:0"`
	QualityIssues           JSONDocument  `json:"qualityIssues,omitempty" gorm:"type:jsonb"`
	InputTokens             int64         `json:"inputTokens" gorm:"not null;default:0"`
	OutputTokens            int64         `json:"outputTokens" gorm:"not null;default:0"`
	TotalTokens             int64         `json:"totalTokens" gorm:"not null;default:0"`
	UsageReported           bool          `json:"usageReported" gorm:"not null;default:false"`
	CostNanoUSD             int64         `json:"costNanoUsd" gorm:"not null;default:0"`
	CostKnown               bool          `json:"costKnown" gorm:"not null;default:false"`
	CreatedAt               time.Time     `json:"createdAt" gorm:"index:summary_versions_conversation_created_idx,priority:2"`
}
