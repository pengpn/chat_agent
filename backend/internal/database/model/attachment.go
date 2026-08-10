package model

import "time"

const (
	AttachmentKindImage    = "image"
	AttachmentKindDocument = "document"

	AttachmentStatusUploaded   = "uploaded"
	AttachmentStatusProcessing = "processing"
	AttachmentStatusReady      = "ready"
	AttachmentStatusFailed     = "failed"
)

// Attachment stores metadata and derived text only. The original bytes live in
// object storage so PostgreSQL rows remain small and independently searchable.
type Attachment struct {
	ID              string        `json:"id" gorm:"type:uuid;default:gen_random_uuid();primaryKey"`
	UserID          string        `json:"-" gorm:"type:uuid;not null;index"`
	User            *User         `json:"-" gorm:"foreignKey:UserID;references:ID;constraint:OnDelete:CASCADE"`
	ConversationID  string        `json:"conversationId" gorm:"type:uuid;not null;index"`
	Conversation    *Conversation `json:"-" gorm:"foreignKey:ConversationID;references:ID;constraint:OnDelete:CASCADE"`
	MessageID       *string       `json:"messageId,omitempty" gorm:"type:uuid;index"`
	Message         *Message      `json:"-" gorm:"foreignKey:MessageID;references:ID;constraint:OnDelete:CASCADE"`
	ObjectKey       string        `json:"-" gorm:"type:text;not null;uniqueIndex"`
	OriginalName    string        `json:"name" gorm:"type:text;not null"`
	Extension       string        `json:"extension" gorm:"type:text;not null"`
	MIMEType        string        `json:"mimeType" gorm:"type:text;not null"`
	Size            int64         `json:"size" gorm:"not null"`
	Kind            string        `json:"kind" gorm:"type:text;not null;check:attachments_kind_check,kind IN ('image','document')"`
	Status          string        `json:"status" gorm:"type:text;not null;check:attachments_status_check,status IN ('uploaded','processing','ready','failed')"`
	ExtractedText   string        `json:"-" gorm:"type:text;not null;default:''"`
	ErrorCode       string        `json:"errorCode,omitempty" gorm:"type:text;not null;default:''"`
	ErrorMessage    string        `json:"errorMessage,omitempty" gorm:"type:text;not null;default:''"`
	ChunkStatus     string        `json:"chunkStatus" gorm:"type:text;not null;default:'pending'"`
	EmbeddingStatus string        `json:"embeddingStatus" gorm:"type:text;not null;default:'pending'"`
	CreatedAt       time.Time     `json:"createdAt"`
	UpdatedAt       time.Time     `json:"updatedAt"`
}

// AttachmentChunk reserves the persistence boundary for later RAG work. An
// embedding column is intentionally deferred until pgvector is introduced.
type AttachmentChunk struct {
	ID              string      `json:"id" gorm:"type:uuid;default:gen_random_uuid();primaryKey"`
	AttachmentID    string      `json:"attachmentId" gorm:"type:uuid;not null;uniqueIndex:attachment_chunk_index"`
	Attachment      *Attachment `json:"-" gorm:"foreignKey:AttachmentID;references:ID;constraint:OnDelete:CASCADE"`
	ChunkIndex      int         `json:"chunkIndex" gorm:"not null;uniqueIndex:attachment_chunk_index"`
	Content         string      `json:"content" gorm:"type:text;not null"`
	TokenCount      int64       `json:"tokenCount" gorm:"not null;default:0"`
	EmbeddingStatus string      `json:"embeddingStatus" gorm:"type:text;not null;default:'pending'"`
	CreatedAt       time.Time   `json:"createdAt"`
	UpdatedAt       time.Time   `json:"updatedAt"`
}
