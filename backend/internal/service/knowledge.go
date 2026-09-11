package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/example/agent-platform/backend/internal/database/model"
	"github.com/example/agent-platform/backend/internal/domain"
	"github.com/example/agent-platform/backend/internal/embeddinggateway"
	"github.com/example/agent-platform/backend/internal/logger"
	"github.com/example/agent-platform/backend/internal/objectstore"
	"github.com/example/agent-platform/backend/internal/repository"
	"github.com/google/uuid"
	pgvector "github.com/pgvector/pgvector-go"
)

type KnowledgeConfig struct {
	MaxBytes          int64
	MaxTextChars      int
	ChunkTargetTokens int64
	ChunkOverlap      int64
	ChunkMinTokens    int64
	EmbeddingBatch    int
	IngestionTimeout  time.Duration
}

type KnowledgeUploadResult struct {
	Document model.KnowledgeDocument `json:"document"`
	Created  bool                    `json:"created"`
}

// KnowledgeService orchestrates source storage, extraction, chunking,
// embedding and atomic vector persistence. Provider SDKs stay behind Gateway.
type KnowledgeService struct {
	repository repository.KnowledgeRepository
	store      objectstore.Store
	embeddings embeddinggateway.Gateway
	estimator  chunkTokenEstimator
	config     KnowledgeConfig
}

func NewKnowledge(repository repository.KnowledgeRepository, store objectstore.Store, embeddings embeddinggateway.Gateway, estimator chunkTokenEstimator, cfg KnowledgeConfig) *KnowledgeService {
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = 20 << 20
	}
	if cfg.MaxTextChars <= 0 {
		cfg.MaxTextChars = 1_000_000
	}
	if cfg.ChunkTargetTokens <= 0 {
		cfg.ChunkTargetTokens = 500
	}
	if cfg.ChunkOverlap < 0 || cfg.ChunkOverlap >= cfg.ChunkTargetTokens {
		cfg.ChunkOverlap = 80
	}
	if cfg.ChunkMinTokens <= 0 {
		cfg.ChunkMinTokens = 40
	}
	if cfg.EmbeddingBatch <= 0 {
		cfg.EmbeddingBatch = 256
	}
	if cfg.IngestionTimeout <= 0 {
		cfg.IngestionTimeout = 10 * time.Minute
	}
	if estimator == nil {
		estimator = embeddinggateway.HeuristicTokenEstimator{}
	}
	return &KnowledgeService{repository: repository, store: store, embeddings: embeddings, estimator: estimator, config: cfg}
}

func (s *KnowledgeService) MaxBytes() int64 { return s.config.MaxBytes }

func (s *KnowledgeService) CreateBase(ctx context.Context, userID, name, description string) (model.KnowledgeBase, error) {
	name, description = strings.TrimSpace(name), strings.TrimSpace(description)
	if name == "" {
		return model.KnowledgeBase{}, ErrKnowledgeNameRequired
	}
	if len([]rune(name)) > 120 {
		return model.KnowledgeBase{}, ErrKnowledgeNameTooLong
	}
	if len([]rune(description)) > 2000 {
		return model.KnowledgeBase{}, ErrKnowledgeDescriptionLong
	}
	base := model.KnowledgeBase{Name: name, Description: description}
	if err := s.repository.CreateBase(ctx, userID, &base); err != nil {
		return model.KnowledgeBase{}, err
	}
	return base, nil
}

func (s *KnowledgeService) ListBases(ctx context.Context, userID string) ([]model.KnowledgeBase, error) {
	return s.repository.ListBasesByUser(ctx, userID)
}

func (s *KnowledgeService) GetBase(ctx context.Context, userID, baseID string) (model.KnowledgeBase, error) {
	base, err := s.repository.FindBaseByIDForUser(ctx, userID, baseID)
	return base, mapKnowledgeBaseNotFound(err)
}

func (s *KnowledgeService) UpdateBase(ctx context.Context, userID, baseID, name, description string) (model.KnowledgeBase, error) {
	name, description = strings.TrimSpace(name), strings.TrimSpace(description)
	if name == "" {
		return model.KnowledgeBase{}, ErrKnowledgeNameRequired
	}
	if len([]rune(name)) > 120 {
		return model.KnowledgeBase{}, ErrKnowledgeNameTooLong
	}
	if len([]rune(description)) > 2000 {
		return model.KnowledgeBase{}, ErrKnowledgeDescriptionLong
	}
	base, err := s.repository.UpdateBase(ctx, userID, baseID, name, description)
	return base, mapKnowledgeBaseNotFound(err)
}

func (s *KnowledgeService) DeleteBase(ctx context.Context, userID, baseID string) error {
	if err := s.repository.DeleteBaseWithCleanup(ctx, userID, baseID); err != nil {
		return mapKnowledgeBaseNotFound(err)
	}
	s.scheduleObjectCleanup(ctx)
	return nil
}

func (s *KnowledgeService) ListDocuments(ctx context.Context, userID, baseID string) ([]model.KnowledgeDocument, error) {
	if _, err := s.repository.FindBaseByIDForUser(ctx, userID, baseID); err != nil {
		return nil, mapKnowledgeBaseNotFound(err)
	}
	return s.repository.ListDocuments(ctx, userID, baseID)
}

func (s *KnowledgeService) GetDocument(ctx context.Context, userID, baseID, documentID string) (model.KnowledgeDocument, error) {
	document, err := s.repository.FindDocumentByIDForUser(ctx, userID, baseID, documentID)
	return document, mapKnowledgeDocumentNotFound(err)
}

func (s *KnowledgeService) Upload(ctx context.Context, userID, baseID, name string, data []byte) (KnowledgeUploadResult, error) {
	if s.embeddings == nil {
		return KnowledgeUploadResult{}, ErrEmbeddingNotConfigured
	}
	if len(data) == 0 {
		return KnowledgeUploadResult{}, ErrAttachmentEmpty
	}
	if int64(len(data)) > s.config.MaxBytes {
		return KnowledgeUploadResult{}, ErrAttachmentTooLarge
	}
	if _, err := s.repository.FindBaseByIDForUser(ctx, userID, baseID); err != nil {
		return KnowledgeUploadResult{}, mapKnowledgeBaseNotFound(err)
	}
	extension := strings.ToLower(filepath.Ext(name))
	mimeType, kind, err := validateAttachment(extension, data)
	if err != nil {
		return KnowledgeUploadResult{}, err
	}
	if kind != model.AttachmentKindDocument {
		return KnowledgeUploadResult{}, ErrAttachmentTypeNotAllowed
	}
	checksumBytes := sha256.Sum256(data)
	checksum := hex.EncodeToString(checksumBytes[:])
	if existing, findErr := s.repository.FindDocumentByChecksumForUser(ctx, userID, baseID, checksum); findErr == nil {
		return KnowledgeUploadResult{Document: existing, Created: false}, nil
	} else if !errors.Is(findErr, domain.ErrNotFound) {
		return KnowledgeUploadResult{}, findErr
	}
	id := uuid.NewString()
	document := model.KnowledgeDocument{
		ID: id, KnowledgeBaseID: baseID, ObjectKey: "knowledge/" + userID + "/" + baseID + "/" + id + extension,
		OriginalName: filepath.Base(name), Extension: extension, MIMEType: mimeType,
		Size: int64(len(data)), Checksum: checksum, Status: model.KnowledgeDocumentStatusUploaded,
	}
	if err := s.store.Put(ctx, document.ObjectKey, mimeType, data); err != nil {
		return KnowledgeUploadResult{}, fmt.Errorf("store knowledge document: %w", err)
	}
	if err := s.repository.CreateDocument(ctx, userID, &document); err != nil {
		_ = s.store.Delete(ctx, document.ObjectKey)
		if errors.Is(err, domain.ErrAlreadyExists) {
			existing, findErr := s.repository.FindDocumentByChecksumForUser(ctx, userID, baseID, checksum)
			if findErr == nil {
				return KnowledgeUploadResult{Document: existing, Created: false}, nil
			}
		}
		return KnowledgeUploadResult{}, err
	}
	if err := s.startIngestion(ctx, userID, baseID, document.ID, []string{model.KnowledgeDocumentStatusUploaded}); err != nil {
		_ = s.repository.DeleteDocumentWithCleanup(context.WithoutCancel(ctx), userID, baseID, document.ID)
		s.scheduleObjectCleanup(ctx)
		return KnowledgeUploadResult{}, err
	}
	if current, getErr := s.GetDocument(ctx, userID, baseID, document.ID); getErr == nil {
		document = current
	}
	return KnowledgeUploadResult{Document: document, Created: true}, nil
}

func (s *KnowledgeService) Retry(ctx context.Context, userID, baseID, documentID string) (model.KnowledgeDocument, error) {
	return s.startAndReturn(ctx, userID, baseID, documentID, []string{model.KnowledgeDocumentStatusFailed})
}

func (s *KnowledgeService) Reindex(ctx context.Context, userID, baseID, documentID string) (model.KnowledgeDocument, error) {
	return s.startAndReturn(ctx, userID, baseID, documentID, []string{model.KnowledgeDocumentStatusReady})
}

func (s *KnowledgeService) startAndReturn(ctx context.Context, userID, baseID, documentID string, statuses []string) (model.KnowledgeDocument, error) {
	if s.embeddings == nil {
		return model.KnowledgeDocument{}, ErrEmbeddingNotConfigured
	}
	if err := s.startIngestion(ctx, userID, baseID, documentID, statuses); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return model.KnowledgeDocument{}, ErrKnowledgeDocumentNotFound
		}
		if errors.Is(err, domain.ErrInvalid) {
			return model.KnowledgeDocument{}, ErrKnowledgeDocumentBusy
		}
		return model.KnowledgeDocument{}, err
	}
	return s.GetDocument(ctx, userID, baseID, documentID)
}

func (s *KnowledgeService) startIngestion(ctx context.Context, userID, baseID, documentID string, statuses []string) error {
	document, err := s.repository.BeginDocumentIngestion(ctx, userID, baseID, documentID, statuses)
	if err != nil {
		return err
	}
	background := context.WithoutCancel(ctx)
	go func() {
		workerCtx, cancel := context.WithTimeout(background, s.config.IngestionTimeout)
		defer cancel()
		s.ingest(workerCtx, userID, document)
	}()
	return nil
}

func (s *KnowledgeService) ingest(ctx context.Context, userID string, document model.KnowledgeDocument) {
	data, err := s.store.Get(ctx, document.ObjectKey)
	if err != nil {
		s.failIngestion(userID, document, "source_unavailable", "无法读取原始文件", err)
		return
	}
	extracted, code, err := extractStructuredDocument(document.Extension, data)
	if err != nil {
		message := "文件解析失败，请确认文件未损坏后重试"
		if code == "no_extractable_text" {
			message = "文档中没有可提取文字；扫描版 PDF 暂不支持 OCR"
		}
		s.failIngestion(userID, document, code, message, err)
		return
	}
	extracted.Text = normalizeDocumentText(extracted.Text)
	if len([]rune(extracted.Text)) > s.config.MaxTextChars {
		s.failIngestion(userID, document, "document_too_long", "提取后的文档内容超过允许长度", errors.New("extracted document exceeds configured character limit"))
		return
	}
	if err := s.repository.AdvanceDocumentIngestion(ctx, userID, document.ID, document.IngestionVersion, model.KnowledgeDocumentStatusExtracting, model.KnowledgeDocumentStatusChunking, extracted.Text); err != nil {
		s.failIngestion(userID, document, "state_conflict", "摄取任务状态已变化", err)
		return
	}
	chunks := buildKnowledgeChunks(extracted, s.estimator, knowledgeChunkConfig{
		TargetTokens: s.config.ChunkTargetTokens, OverlapTokens: s.config.ChunkOverlap, MinTokens: s.config.ChunkMinTokens,
	})
	if len(chunks) == 0 {
		s.failIngestion(userID, document, "no_chunks", "文档没有生成有效分块", errors.New("chunker returned no chunks"))
		return
	}
	if err := s.repository.AdvanceDocumentIngestion(ctx, userID, document.ID, document.IngestionVersion, model.KnowledgeDocumentStatusChunking, model.KnowledgeDocumentStatusEmbedding, ""); err != nil {
		s.failIngestion(userID, document, "state_conflict", "摄取任务状态已变化", err)
		return
	}
	providerName, embeddingModel, dimension := "", "", 0
	for start := 0; start < len(chunks); start += s.config.EmbeddingBatch {
		end := start + s.config.EmbeddingBatch
		if end > len(chunks) {
			end = len(chunks)
		}
		texts := make([]string, end-start)
		for index := start; index < end; index++ {
			texts[index-start] = chunks[index].Content
		}
		result, embedErr := s.embeddings.EmbedDocuments(ctx, embeddinggateway.Request{
			OperationID: fmt.Sprintf("knowledge:%s:v%d:%d", document.ID, document.IngestionVersion, start/s.config.EmbeddingBatch),
			Purpose:     "rag_document", UserID: userID, Texts: texts,
		})
		if embedErr != nil {
			s.failIngestion(userID, document, "embedding_failed", "向量生成失败，请稍后重试", embedErr)
			return
		}
		if start == 0 {
			providerName, embeddingModel, dimension = result.Provider, result.Model, result.Dimension
		} else if result.Provider != providerName || result.Model != embeddingModel || result.Dimension != dimension {
			s.failIngestion(userID, document, "embedding_version_changed", "摄取过程中向量模型配置发生变化", errors.New("embedding metadata changed between batches"))
			return
		}
		for _, vector := range result.Vectors {
			chunkIndex := start + vector.Index
			if chunkIndex < start || chunkIndex >= end {
				s.failIngestion(userID, document, "embedding_invalid", "向量结果与分块不匹配", errors.New("embedding index outside batch"))
				return
			}
			value := pgvector.NewVector(vector.Values)
			chunks[chunkIndex].Embedding = &value
			chunks[chunkIndex].EmbeddingModel = result.Model
			chunks[chunkIndex].EmbeddingDimension = result.Dimension
		}
	}
	if err := s.repository.CompleteDocumentIngestion(ctx, userID, document.ID, document.IngestionVersion, chunks, providerName, embeddingModel, dimension, time.Now()); err != nil {
		s.failIngestion(userID, document, "persist_failed", "保存向量索引失败，请稍后重试", err)
		return
	}
	logger.Info(ctx, "knowledge document ingestion completed", logger.String("user_id", userID), logger.String("document_id", document.ID), logger.Int("chunk_count", len(chunks)), logger.Int64("ingestion_version", document.IngestionVersion))
}

func (s *KnowledgeService) failIngestion(userID string, document model.KnowledgeDocument, code, message string, cause error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.repository.FailDocumentIngestion(ctx, userID, document.ID, document.IngestionVersion, code, message); err != nil && !errors.Is(err, domain.ErrInvalid) {
		logger.Error(ctx, "persist knowledge ingestion failure failed", logger.String("document_id", document.ID), logger.Err(err))
	}
	logger.Warn(ctx, "knowledge document ingestion failed", logger.String("user_id", userID), logger.String("document_id", document.ID), logger.String("error_code", code), logger.Int64("ingestion_version", document.IngestionVersion), logger.Err(cause))
}

func (s *KnowledgeService) DeleteDocument(ctx context.Context, userID, baseID, documentID string) error {
	if err := s.repository.DeleteDocumentWithCleanup(ctx, userID, baseID, documentID); err != nil {
		return mapKnowledgeDocumentNotFound(err)
	}
	s.scheduleObjectCleanup(ctx)
	return nil
}

func (s *KnowledgeService) Content(ctx context.Context, userID, baseID, documentID string) (model.KnowledgeDocument, []byte, error) {
	document, err := s.GetDocument(ctx, userID, baseID, documentID)
	if err != nil {
		return model.KnowledgeDocument{}, nil, err
	}
	data, err := s.store.Get(ctx, document.ObjectKey)
	return document, data, err
}

func (s *KnowledgeService) ListChunks(ctx context.Context, userID, baseID, documentID string) ([]model.KnowledgeChunk, error) {
	if _, err := s.GetDocument(ctx, userID, baseID, documentID); err != nil {
		return nil, err
	}
	return s.repository.ListChunks(ctx, userID, documentID)
}

func (s *KnowledgeService) DrainObjectCleanup(ctx context.Context) { s.drainObjectCleanup(ctx, 100) }

func (s *KnowledgeService) scheduleObjectCleanup(ctx context.Context) {
	background := context.WithoutCancel(ctx)
	go func() {
		cleanupCtx, cancel := context.WithTimeout(background, 10*time.Second)
		defer cancel()
		s.drainObjectCleanup(cleanupCtx, 100)
	}()
}

func (s *KnowledgeService) drainObjectCleanup(ctx context.Context, limit int) {
	items, err := s.repository.ListPendingObjectCleanup(ctx, limit)
	if err != nil {
		logger.Error(ctx, "list knowledge object cleanup failed", logger.Err(err))
		return
	}
	for _, item := range items {
		if err := s.store.Delete(ctx, item.ObjectKey); err != nil {
			_ = s.repository.FailObjectCleanup(ctx, item.ID, err.Error())
			logger.Warn(ctx, "knowledge object cleanup failed", logger.String("cleanup_id", item.ID), logger.String("object_key", item.ObjectKey), logger.Err(err))
			continue
		}
		if err := s.repository.CompleteObjectCleanup(ctx, item.ID); err != nil {
			logger.Error(ctx, "complete knowledge object cleanup failed", logger.String("cleanup_id", item.ID), logger.Err(err))
		}
	}
}

func mapKnowledgeBaseNotFound(err error) error {
	if errors.Is(err, domain.ErrNotFound) {
		return ErrKnowledgeBaseNotFound
	}
	return err
}

func mapKnowledgeDocumentNotFound(err error) error {
	if errors.Is(err, domain.ErrNotFound) {
		return ErrKnowledgeDocumentNotFound
	}
	return err
}
