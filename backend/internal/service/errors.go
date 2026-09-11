// Package service implements application business rules and transaction orchestration.
package service

import (
	"errors"
	"time"
)

var (
	ErrInvalidEmail              = errors.New("invalid email")
	ErrPasswordTooShort          = errors.New("password too short")
	ErrPasswordTooLong           = errors.New("password too long")
	ErrPasswordUnchanged         = errors.New("password unchanged")
	ErrDisplayNameTooLong        = errors.New("display name too long")
	ErrEmailAlreadyRegistered    = errors.New("email already registered")
	ErrInvalidCredentials        = errors.New("invalid credentials")
	ErrUnauthenticated           = errors.New("unauthenticated")
	ErrInvalidRefreshToken       = errors.New("invalid refresh token")
	ErrInvalidAccountToken       = errors.New("invalid account token")
	ErrEmailDeliveryUnavailable  = errors.New("email delivery unavailable")
	ErrSessionNotFound           = errors.New("session not found")
	ErrAccountDeleteConfirmation = errors.New("account delete confirmation invalid")
	ErrConversationNotFound      = errors.New("conversation not found")
	ErrEmptyConversationTitle    = errors.New("empty conversation title")
	ErrInvalidMessageLength      = errors.New("invalid message length")
	ErrInvalidTemperature        = errors.New("invalid temperature")
	ErrOutputFormatNotFound      = errors.New("output format not found")
	ErrMessageNotFound           = errors.New("message not found")
	ErrMessageNotRegenerable     = errors.New("message cannot be regenerated")
	ErrMessageNotEditable        = errors.New("message cannot be edited")
	ErrInvalidCursor             = errors.New("invalid pagination cursor")
	ErrInvalidPageLimit          = errors.New("invalid page limit")
	ErrAttachmentEmpty           = errors.New("attachment is empty")
	ErrAttachmentTooLarge        = errors.New("attachment is too large")
	ErrAttachmentTypeNotAllowed  = errors.New("attachment type is not allowed")
	ErrAttachmentMIMEMismatch    = errors.New("attachment MIME does not match extension")
	ErrAttachmentNotFound        = errors.New("attachment not found")
	ErrAttachmentNotReady        = errors.New("attachment is not ready")
	ErrAttachmentAlreadyUsed     = errors.New("attachment already belongs to a message")
	ErrTooManyAttachments        = errors.New("too many attachments")
	ErrModelDoesNotSupportVision = errors.New("model does not support image input")
	ErrKnowledgeBaseNotFound     = errors.New("knowledge base not found")
	ErrKnowledgeDocumentNotFound = errors.New("knowledge document not found")
	ErrKnowledgeNameRequired     = errors.New("knowledge base name is required")
	ErrKnowledgeNameTooLong      = errors.New("knowledge base name is too long")
	ErrKnowledgeDescriptionLong  = errors.New("knowledge base description is too long")
	ErrKnowledgeDocumentBusy     = errors.New("knowledge document ingestion is already running")
	ErrEmbeddingNotConfigured    = errors.New("embedding gateway is not configured")
	// 额度与并发准入错误：预扣失败时直接拒绝请求，不创建任何消息。
	ErrDailyQuotaExceeded           = errors.New("daily quota exceeded")
	ErrMonthlyQuotaExceeded         = errors.New("monthly quota exceeded")
	ErrTooManyConcurrentGenerations = errors.New("too many concurrent generations")
	ErrContentBlockedByModeration   = errors.New("content blocked by moderation")
	ErrAccountBanned                = errors.New("account banned")
	ErrAdminSelfOperation           = errors.New("admin cannot perform this operation on self")
	ErrInvalidQuotaLimit            = errors.New("invalid quota limit")
)

type RateLimitError struct{ RetryAfter time.Duration }

func (e *RateLimitError) Error() string { return "rate limit exceeded" }

// ProviderSelectionError indicates a client selected a provider or model that
// is unavailable in the current workspace.
type ProviderSelectionError struct{ err error }

func (e *ProviderSelectionError) Error() string { return e.err.Error() }

func (e *ProviderSelectionError) Unwrap() error { return e.err }
