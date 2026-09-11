package model

import (
	"time"

	pgvector "github.com/pgvector/pgvector-go"
)

const (
	KnowledgeDocumentStatusUploaded   = "uploaded"
	KnowledgeDocumentStatusExtracting = "extracting"
	KnowledgeDocumentStatusChunking   = "chunking"
	KnowledgeDocumentStatusEmbedding  = "embedding"
	KnowledgeDocumentStatusReady      = "ready"
	KnowledgeDocumentStatusFailed     = "failed"
)

// KnowledgeBase is a user-owned collection of reusable documents. It is not
// tied to a conversation, so the same knowledge can be selected in many chats.
type KnowledgeBase struct {
	ID          string              `json:"id" gorm:"type:uuid;default:gen_random_uuid();primaryKey"`
	UserID      string              `json:"-" gorm:"type:uuid;not null;index:knowledge_bases_user_updated_idx,priority:1"`
	User        *User               `json:"-" gorm:"foreignKey:UserID;references:ID;constraint:OnDelete:CASCADE"`
	Name        string              `json:"name" gorm:"type:varchar(120);not null"`
	Description string              `json:"description" gorm:"type:text;not null;default:''"`
	Documents   []KnowledgeDocument `json:"-" gorm:"foreignKey:KnowledgeBaseID;constraint:OnDelete:CASCADE"`
	CreatedAt   time.Time           `json:"createdAt"`
	UpdatedAt   time.Time           `json:"updatedAt" gorm:"index:knowledge_bases_user_updated_idx,priority:2,sort:desc"`
}

// KnowledgeDocument stores ingestion state and source metadata. The original
// bytes live in object storage while extracted text remains internal.
type KnowledgeDocument struct {
	ID                 string         `json:"id" gorm:"type:uuid;default:gen_random_uuid();primaryKey"`
	KnowledgeBaseID    string         `json:"knowledgeBaseId" gorm:"type:uuid;not null;index;uniqueIndex:knowledge_documents_base_checksum_idx,priority:1"`
	KnowledgeBase      *KnowledgeBase `json:"-" gorm:"foreignKey:KnowledgeBaseID;references:ID;constraint:OnDelete:CASCADE"`
	ObjectKey          string         `json:"-" gorm:"type:text;not null;uniqueIndex"`
	OriginalName       string         `json:"name" gorm:"type:text;not null"`
	Extension          string         `json:"extension" gorm:"type:varchar(20);not null"`
	MIMEType           string         `json:"mimeType" gorm:"type:text;not null"`
	Size               int64          `json:"size" gorm:"not null"`
	Checksum           string         `json:"checksum" gorm:"type:char(64);not null;uniqueIndex:knowledge_documents_base_checksum_idx,priority:2"`
	Status             string         `json:"status" gorm:"type:varchar(24);not null;default:uploaded;index;check:knowledge_documents_status_check,status IN ('uploaded','extracting','chunking','embedding','ready','failed')"`
	ExtractedText      string         `json:"-" gorm:"type:text;not null;default:''"`
	ErrorCode          string         `json:"errorCode,omitempty" gorm:"type:varchar(80);not null;default:''"`
	ErrorMessage       string         `json:"errorMessage,omitempty" gorm:"type:text;not null;default:''"`
	ChunkCount         int            `json:"chunkCount" gorm:"not null;default:0"`
	EmbeddingProvider  string         `json:"embeddingProvider,omitempty" gorm:"type:varchar(40);not null;default:''"`
	EmbeddingModel     string         `json:"embeddingModel,omitempty" gorm:"type:text;not null;default:''"`
	EmbeddingDimension int            `json:"embeddingDimension,omitempty" gorm:"not null;default:0"`
	IngestionVersion   int64          `json:"ingestionVersion" gorm:"not null;default:0"`
	IndexedAt          *time.Time     `json:"indexedAt,omitempty"`
	CreatedAt          time.Time      `json:"createdAt"`
	UpdatedAt          time.Time      `json:"updatedAt"`
}

// KnowledgeChunk is the smallest retrievable unit. Embedding is nullable
// while ingestion is in progress. An unconstrained vector column lets the
// next stage configure the embedding model and dimension explicitly.
type KnowledgeChunk struct {
	ID                 string             `json:"id" gorm:"type:uuid;default:gen_random_uuid();primaryKey"`
	DocumentID         string             `json:"documentId" gorm:"type:uuid;not null;uniqueIndex:knowledge_chunks_document_index_idx,priority:1;index"`
	Document           *KnowledgeDocument `json:"-" gorm:"foreignKey:DocumentID;references:ID;constraint:OnDelete:CASCADE"`
	ChunkIndex         int                `json:"chunkIndex" gorm:"not null;uniqueIndex:knowledge_chunks_document_index_idx,priority:2"`
	Content            string             `json:"content" gorm:"type:text;not null"`
	ContentHash        string             `json:"contentHash" gorm:"type:char(64);not null"`
	TokenCount         int64              `json:"tokenCount" gorm:"not null;default:0"`
	PageNumber         *int               `json:"pageNumber,omitempty"`
	SectionTitle       string             `json:"sectionTitle,omitempty" gorm:"type:text;not null;default:''"`
	Metadata           JSONDocument       `json:"metadata,omitempty" gorm:"type:jsonb"`
	Embedding          *pgvector.Vector   `json:"-" gorm:"type:vector"`
	EmbeddingModel     string             `json:"embeddingModel,omitempty" gorm:"type:text;not null;default:''"`
	EmbeddingDimension int                `json:"embeddingDimension,omitempty" gorm:"not null;default:0"`
	CreatedAt          time.Time          `json:"createdAt"`
	UpdatedAt          time.Time          `json:"updatedAt"`
}

// MessageCitation keeps a source snapshot with an assistant answer. Nullable
// source IDs allow a historical citation to remain readable after a user
// deletes the original knowledge document.
type MessageCitation struct {
	ID              string             `json:"id" gorm:"type:uuid;default:gen_random_uuid();primaryKey"`
	MessageID       string             `json:"messageId" gorm:"type:uuid;not null;uniqueIndex:message_citations_message_ordinal_idx,priority:1;index"`
	Message         *Message           `json:"-" gorm:"foreignKey:MessageID;references:ID;constraint:OnDelete:CASCADE"`
	KnowledgeBaseID *string            `json:"knowledgeBaseId,omitempty" gorm:"type:uuid;index"`
	KnowledgeBase   *KnowledgeBase     `json:"-" gorm:"foreignKey:KnowledgeBaseID;references:ID;constraint:OnDelete:SET NULL"`
	DocumentID      *string            `json:"documentId,omitempty" gorm:"type:uuid;index"`
	Document        *KnowledgeDocument `json:"-" gorm:"foreignKey:DocumentID;references:ID;constraint:OnDelete:SET NULL"`
	ChunkID         *string            `json:"chunkId,omitempty" gorm:"type:uuid;index"`
	Chunk           *KnowledgeChunk    `json:"-" gorm:"foreignKey:ChunkID;references:ID;constraint:OnDelete:SET NULL"`
	Ordinal         int                `json:"ordinal" gorm:"not null;uniqueIndex:message_citations_message_ordinal_idx,priority:2"`
	SourceName      string             `json:"sourceName" gorm:"type:text;not null"`
	PageNumber      *int               `json:"pageNumber,omitempty"`
	SectionTitle    string             `json:"sectionTitle,omitempty" gorm:"type:text;not null;default:''"`
	Quote           string             `json:"quote" gorm:"type:text;not null"`
	SimilarityScore float64            `json:"similarityScore" gorm:"not null;default:0"`
	CreatedAt       time.Time          `json:"createdAt"`
}

// KnowledgeObjectCleanup is a small transactional outbox for object storage
// deletion. The database row is committed together with the logical delete;
// a later retry can still remove the source object if MinIO was unavailable.
type KnowledgeObjectCleanup struct {
	ID        string    `json:"id" gorm:"type:uuid;default:gen_random_uuid();primaryKey"`
	ObjectKey string    `json:"objectKey" gorm:"type:text;not null;uniqueIndex"`
	Attempts  int       `json:"attempts" gorm:"not null;default:0"`
	LastError string    `json:"lastError,omitempty" gorm:"type:text;not null;default:''"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}
