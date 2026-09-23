package model

import "time"

// Message is the persisted message model.
type Message struct {
	ID               string            `json:"id" gorm:"type:uuid;default:gen_random_uuid();primaryKey;index:messages_conversation_created_idx,priority:3,sort:desc"`
	ConversationID   string            `json:"conversationId" gorm:"type:uuid;not null;index:messages_conversation_created_idx,priority:1"`
	Conversation     *Conversation     `json:"-" gorm:"foreignKey:ConversationID;references:ID;constraint:OnDelete:CASCADE"`
	Role             string            `json:"role" gorm:"type:text;not null;check:messages_role_check,role IN ('system', 'user', 'assistant', 'tool')"`
	Content          string            `json:"content" gorm:"type:text;not null;default:''"`
	Think            string            `json:"think,omitempty" gorm:"type:text;not null;default:''"`
	ParentMessageID  *string           `json:"parentMessageId,omitempty" gorm:"type:uuid;index"`
	IsActiveChild    bool              `json:"isActiveChild,omitempty" gorm:"not null;default:false;index"`
	EditedFrom       *string           `json:"editedFromMessageId,omitempty" gorm:"type:uuid;index"`
	ReplyToMessageID *string           `json:"replyToMessageId,omitempty" gorm:"type:uuid;index"`
	ResponseVersion  int64             `json:"responseVersion,omitempty" gorm:"not null;default:0"`
	IsActiveResponse bool              `json:"isActiveResponse,omitempty" gorm:"not null;default:false;index"`
	RegeneratedFrom  *string           `json:"regeneratedFromMessageId,omitempty" gorm:"type:uuid;index"`
	GenerationKey    *string           `json:"-" gorm:"type:text;uniqueIndex"`
	KnowledgeBaseIDs JSONStringArray   `json:"knowledgeBaseIds,omitempty" gorm:"type:jsonb"`
	StructuredOutput JSONDocument      `json:"structuredOutput,omitempty" gorm:"type:jsonb"`
	Provider         string            `json:"provider,omitempty" gorm:"type:text"`
	Model            string            `json:"model,omitempty" gorm:"type:text"`
	Status           string            `json:"status" gorm:"type:text;not null;default:complete;check:messages_status_check,status IN ('streaming', 'complete', 'stopped', 'interrupted', 'error')"`
	FinishReason     string            `json:"finishReason,omitempty" gorm:"type:text;not null;default:''"`
	InputTokens      int64             `json:"inputTokens" gorm:"not null;default:0"`
	OutputTokens     int64             `json:"outputTokens" gorm:"not null;default:0"`
	TotalTokens      int64             `json:"totalTokens" gorm:"not null;default:0"`
	UsageReported    bool              `json:"usageReported" gorm:"not null;default:false"`
	CostNanoUSD      int64             `json:"costNanoUsd" gorm:"not null;default:0"`
	CostKnown        bool              `json:"costKnown" gorm:"not null;default:false"`
	Attachments      []Attachment      `json:"attachments,omitempty" gorm:"foreignKey:MessageID"`
	Citations        []MessageCitation `json:"citations,omitempty" gorm:"foreignKey:MessageID;constraint:OnDelete:CASCADE"`
	CreatedAt        time.Time         `json:"createdAt" gorm:"index:messages_conversation_created_idx,priority:2,sort:desc"`
	UpdatedAt        time.Time         `json:"-"`
}

type MessageCompletion struct {
	Content          string
	Think            string
	StructuredOutput JSONDocument
	Status           string
	FinishReason     string
	Usage            MessageUsage
	Citations        []MessageCitation
}

// MessageUsage is the final accounting data saved with an assistant message.
// CostNanoUSD represents one billionth of a US dollar, avoiding float-based
// monetary persistence (1 USD = 1,000,000,000 nano USD).
type MessageUsage struct {
	InputTokens   int64
	OutputTokens  int64
	TotalTokens   int64
	UsageReported bool
	CostNanoUSD   int64
	CostKnown     bool
}
