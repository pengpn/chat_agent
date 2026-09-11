package service

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/example/agent-platform/backend/internal/database/model"
	"github.com/example/agent-platform/backend/internal/domain"
	"github.com/example/agent-platform/backend/internal/objectstore"
	"github.com/example/agent-platform/backend/internal/provider"
	"github.com/example/agent-platform/backend/internal/repository"
	"github.com/google/uuid"
)

type AttachmentConfig struct {
	MaxBytes               int64
	MaxCount, MaxTextChars int
}

type VisionCapability interface {
	SupportsVision(providerID, model string) bool
}

type AttachmentService struct {
	conversations repository.ConversationRepository
	repository    repository.AttachmentRepository
	store         objectstore.Store
	vision        VisionCapability
	config        AttachmentConfig
}

type PreparedAttachments struct {
	Items        []model.Attachment
	Parts        []provider.ContentPart
	DocumentText string
}

func NewAttachment(conversations repository.ConversationRepository, repository repository.AttachmentRepository, store objectstore.Store, vision VisionCapability, cfg AttachmentConfig) *AttachmentService {
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = 10 << 20
	}
	if cfg.MaxCount <= 0 {
		cfg.MaxCount = 5
	}
	if cfg.MaxTextChars <= 0 {
		cfg.MaxTextChars = 200_000
	}
	return &AttachmentService{conversations: conversations, repository: repository, store: store, vision: vision, config: cfg}
}

func (s *AttachmentService) MaxBytes() int64 { return s.config.MaxBytes }

func (s *AttachmentService) Upload(ctx context.Context, userID, conversationID, name string, data []byte) (model.Attachment, error) {
	if s.conversations != nil {
		if _, err := s.conversations.FindByIDForUser(ctx, userID, conversationID); errors.Is(err, domain.ErrNotFound) {
			return model.Attachment{}, ErrConversationNotFound
		} else if err != nil {
			return model.Attachment{}, err
		}
	}
	if int64(len(data)) == 0 {
		return model.Attachment{}, ErrAttachmentEmpty
	}
	if int64(len(data)) > s.config.MaxBytes {
		return model.Attachment{}, ErrAttachmentTooLarge
	}
	extension := strings.ToLower(filepath.Ext(name))
	mimeType, kind, err := validateAttachment(extension, data)
	if err != nil {
		return model.Attachment{}, err
	}
	id := uuid.NewString()
	item := model.Attachment{
		ID: id, UserID: userID, ConversationID: conversationID,
		ObjectKey:    userID + "/" + conversationID + "/" + id + extension,
		OriginalName: filepath.Base(name), Extension: extension, MIMEType: mimeType,
		Size: int64(len(data)), Kind: kind, Status: model.AttachmentStatusUploaded,
	}
	if err := s.store.Put(ctx, item.ObjectKey, item.MIMEType, data); err != nil {
		return model.Attachment{}, fmt.Errorf("store attachment: %w", err)
	}
	if err := s.repository.Create(ctx, &item); err != nil {
		_ = s.store.Delete(ctx, item.ObjectKey)
		return model.Attachment{}, err
	}
	return s.parse(ctx, item, data)
}

func (s *AttachmentService) Retry(ctx context.Context, userID, conversationID, id string) (model.Attachment, error) {
	item, err := s.repository.FindByIDForUser(ctx, userID, conversationID, id)
	if err != nil {
		return model.Attachment{}, mapAttachmentNotFound(err)
	}
	if item.MessageID != nil {
		return model.Attachment{}, ErrAttachmentAlreadyUsed
	}
	data, err := s.store.Get(ctx, item.ObjectKey)
	if err != nil {
		return model.Attachment{}, fmt.Errorf("read attachment: %w", err)
	}
	return s.parse(ctx, item, data)
}

func (s *AttachmentService) parse(ctx context.Context, item model.Attachment, data []byte) (model.Attachment, error) {
	item, err := s.repository.UpdateParsing(ctx, item.UserID, item.ConversationID, item.ID, model.AttachmentStatusProcessing, "", "", "")
	if err != nil {
		return model.Attachment{}, err
	}
	if item.Kind == model.AttachmentKindImage {
		return s.repository.UpdateParsing(ctx, item.UserID, item.ConversationID, item.ID, model.AttachmentStatusReady, "", "", "")
	}
	text, code, err := extractDocument(item.Extension, data)
	if err != nil {
		message := "文件解析失败，请确认文件未损坏后重试"
		if code == "no_extractable_text" {
			message = "PDF 中没有可提取文字；扫描版 PDF 暂不支持 OCR"
		}
		failed, updateErr := s.repository.UpdateParsing(ctx, item.UserID, item.ConversationID, item.ID, model.AttachmentStatusFailed, "", code, message)
		if updateErr != nil {
			return model.Attachment{}, updateErr
		}
		return failed, nil
	}
	runes := []rune(strings.TrimSpace(text))
	if len(runes) > s.config.MaxTextChars {
		runes = runes[:s.config.MaxTextChars]
	}
	return s.repository.UpdateParsing(ctx, item.UserID, item.ConversationID, item.ID, model.AttachmentStatusReady, string(runes), "", "")
}

func (s *AttachmentService) PrepareForChat(ctx context.Context, userID, conversationID, providerID, modelName string, ids []string) (PreparedAttachments, error) {
	ids = uniqueStrings(ids)
	if len(ids) == 0 {
		return PreparedAttachments{}, nil
	}
	if len(ids) > s.config.MaxCount {
		return PreparedAttachments{}, ErrTooManyAttachments
	}
	items, err := s.repository.FindManyForUser(ctx, userID, conversationID, ids)
	if err != nil {
		return PreparedAttachments{}, err
	}
	if len(items) != len(ids) {
		return PreparedAttachments{}, ErrAttachmentNotFound
	}
	byID := make(map[string]model.Attachment, len(items))
	for _, item := range items {
		byID[item.ID] = item
	}
	result := PreparedAttachments{Items: make([]model.Attachment, 0, len(ids))}
	var documents strings.Builder
	for _, id := range ids {
		item := byID[id]
		if item.MessageID != nil {
			return PreparedAttachments{}, ErrAttachmentAlreadyUsed
		}
		if item.Status != model.AttachmentStatusReady {
			return PreparedAttachments{}, ErrAttachmentNotReady
		}
		result.Items = append(result.Items, item)
		if item.Kind == model.AttachmentKindImage {
			if s.vision == nil || !s.vision.SupportsVision(providerID, modelName) {
				return PreparedAttachments{}, ErrModelDoesNotSupportVision
			}
			data, getErr := s.store.Get(ctx, item.ObjectKey)
			if getErr != nil {
				return PreparedAttachments{}, getErr
			}
			result.Parts = append(result.Parts, provider.ContentPart{Type: "image", MediaType: item.MIMEType, Data: base64.StdEncoding.EncodeToString(data)})
		} else {
			fmt.Fprintf(&documents, "\n\n--- 附件：%s ---\n%s\n--- 附件结束 ---", item.OriginalName, item.ExtractedText)
		}
	}
	result.DocumentText = documents.String()
	return result, nil
}

// PrepareStoredForChat rehydrates attachments already bound to a user message,
// for example when regenerating its assistant answer. The message itself came
// from a user-scoped repository query.
func (s *AttachmentService) PrepareStoredForChat(ctx context.Context, providerID, modelName string, items []model.Attachment) (PreparedAttachments, error) {
	result := PreparedAttachments{Items: items}
	var documents strings.Builder
	for _, item := range items {
		if item.Status != model.AttachmentStatusReady {
			return PreparedAttachments{}, ErrAttachmentNotReady
		}
		if item.Kind == model.AttachmentKindImage {
			if s.vision == nil || !s.vision.SupportsVision(providerID, modelName) {
				return PreparedAttachments{}, ErrModelDoesNotSupportVision
			}
			data, err := s.store.Get(ctx, item.ObjectKey)
			if err != nil {
				return PreparedAttachments{}, err
			}
			result.Parts = append(result.Parts, provider.ContentPart{Type: "image", MediaType: item.MIMEType, Data: base64.StdEncoding.EncodeToString(data)})
		} else {
			fmt.Fprintf(&documents, "\n\n--- 附件：%s ---\n%s\n--- 附件结束 ---", item.OriginalName, item.ExtractedText)
		}
	}
	result.DocumentText = documents.String()
	return result, nil
}

func (s *AttachmentService) DeletePending(ctx context.Context, userID, conversationID, id string) error {
	item, err := s.repository.DeletePending(ctx, userID, conversationID, id)
	if err != nil {
		return mapAttachmentNotFound(err)
	}
	return s.store.Delete(ctx, item.ObjectKey)
}

func (s *AttachmentService) CleanupConversation(ctx context.Context, userID, conversationID string) ([]string, error) {
	return s.repository.ListObjectKeysByConversation(ctx, userID, conversationID)
}

func (s *AttachmentService) DeleteObjects(ctx context.Context, keys []string) {
	for _, key := range keys {
		_ = s.store.Delete(ctx, key)
	}
}

func (s *AttachmentService) Content(ctx context.Context, userID, conversationID, id string) (model.Attachment, []byte, error) {
	item, err := s.repository.FindByIDForUser(ctx, userID, conversationID, id)
	if err != nil {
		return model.Attachment{}, nil, mapAttachmentNotFound(err)
	}
	data, err := s.store.Get(ctx, item.ObjectKey)
	return item, data, err
}

func validateAttachment(extension string, data []byte) (string, string, error) {
	detected := http.DetectContentType(data)
	allowed := map[string]struct{ mime, kind string }{
		".png": {"image/png", model.AttachmentKindImage}, ".jpg": {"image/jpeg", model.AttachmentKindImage},
		".jpeg": {"image/jpeg", model.AttachmentKindImage}, ".gif": {"image/gif", model.AttachmentKindImage},
		".webp": {"image/webp", model.AttachmentKindImage}, ".txt": {"text/plain", model.AttachmentKindDocument},
		".pdf": {"application/pdf", model.AttachmentKindDocument}, ".docx": {"application/vnd.openxmlformats-officedocument.wordprocessingml.document", model.AttachmentKindDocument},
	}
	target, ok := allowed[extension]
	if !ok {
		return "", "", ErrAttachmentTypeNotAllowed
	}
	actual := detected
	if extension == ".docx" && (detected == "application/zip" || detected == "application/octet-stream") && isDOCX(data) {
		actual = target.mime
	}
	if extension == ".webp" && len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		actual = target.mime
	}
	if extension == ".txt" && (detected == "application/octet-stream" || strings.HasPrefix(detected, "text/plain")) && utf8.Valid(data) {
		actual = target.mime
	}
	if actual != target.mime {
		return "", "", ErrAttachmentMIMEMismatch
	}
	if target.kind == model.AttachmentKindImage && extension != ".webp" {
		if _, _, err := image.DecodeConfig(bytes.NewReader(data)); err != nil {
			return "", "", ErrAttachmentMIMEMismatch
		}
	}
	return target.mime, target.kind, nil
}

func isDOCX(data []byte) bool {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return false
	}
	for _, file := range reader.File {
		if file.Name == "word/document.xml" {
			return true
		}
	}
	return false
}

func extractDocument(extension string, data []byte) (string, string, error) {
	// Chat attachments preserve the user's original TXT formatting. Knowledge
	// ingestion uses the richer structured representation for chunk metadata.
	if extension == ".txt" {
		return string(data), "", nil
	}
	document, code, err := extractStructuredDocument(extension, data)
	if extension == ".docx" {
		text := strings.ReplaceAll(document.Text, "\n\n", "\n")
		if text != "" {
			text += "\n"
		}
		return text, code, err
	}
	return document.Text, code, err
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}

func mapAttachmentNotFound(err error) error {
	if errors.Is(err, domain.ErrNotFound) {
		return ErrAttachmentNotFound
	}
	return err
}
