package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/example/agent-platform/backend/internal/embeddinggateway"
	"github.com/example/agent-platform/backend/internal/logger"
	"github.com/example/agent-platform/backend/internal/repository"
	"github.com/google/uuid"
	pgvector "github.com/pgvector/pgvector-go"
)

type KnowledgeRetrievalConfig struct {
	CandidateLimit      int
	TopK                int
	SimilarityThreshold float64
	ContextTokenBudget  int64
	MaxKnowledgeBases   int
	MaxQueryChars       int
}

type KnowledgeRetrievalRequest struct {
	Query            string   `json:"query"`
	KnowledgeBaseIDs []string `json:"knowledgeBaseIds"`
}

type KnowledgeRetrievalMatch struct {
	Rank            int            `json:"rank"`
	KnowledgeBaseID string         `json:"knowledgeBaseId"`
	DocumentID      string         `json:"documentId"`
	DocumentName    string         `json:"documentName"`
	ChunkID         string         `json:"chunkId"`
	ChunkIndex      int            `json:"chunkIndex"`
	Content         string         `json:"content"`
	ContentHash     string         `json:"contentHash"`
	TokenCount      int64          `json:"tokenCount"`
	ContextTokens   int64          `json:"contextTokens"`
	PageNumber      *int           `json:"pageNumber,omitempty"`
	SectionTitle    string         `json:"sectionTitle,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty"`
	Similarity      float64        `json:"similarity"`
}

type KnowledgeRetrievalResult struct {
	Matches            []KnowledgeRetrievalMatch `json:"matches"`
	CandidateCount     int                       `json:"candidateCount"`
	UsedContextTokens  int64                     `json:"usedContextTokens"`
	ContextTokenBudget int64                     `json:"contextTokenBudget"`
	EmbeddingProvider  string                    `json:"embeddingProvider"`
	EmbeddingModel     string                    `json:"embeddingModel"`
	EmbeddingDimension int                       `json:"embeddingDimension"`
}

// KnowledgeRetrievalService is intentionally separate from ingestion. It
// proves retrieval quality without coupling search to chat generation.
type KnowledgeRetrievalService struct {
	repository repository.KnowledgeRepository
	embeddings embeddinggateway.Gateway
	estimator  chunkTokenEstimator
	config     KnowledgeRetrievalConfig
}

func NewKnowledgeRetrieval(repository repository.KnowledgeRepository, embeddings embeddinggateway.Gateway, estimator chunkTokenEstimator, cfg KnowledgeRetrievalConfig) *KnowledgeRetrievalService {
	if cfg.CandidateLimit <= 0 {
		cfg.CandidateLimit = 30
	}
	if cfg.TopK <= 0 {
		cfg.TopK = 5
	}
	if cfg.SimilarityThreshold < 0 || cfg.SimilarityThreshold > 1 {
		cfg.SimilarityThreshold = 0.35
	}
	if cfg.ContextTokenBudget <= 0 {
		cfg.ContextTokenBudget = 3000
	}
	if cfg.MaxKnowledgeBases <= 0 {
		cfg.MaxKnowledgeBases = 8
	}
	if cfg.MaxQueryChars <= 0 {
		cfg.MaxQueryChars = 4000
	}
	if estimator == nil {
		estimator = embeddinggateway.HeuristicTokenEstimator{}
	}
	return &KnowledgeRetrievalService{repository: repository, embeddings: embeddings, estimator: estimator, config: cfg}
}

func (s *KnowledgeRetrievalService) Search(ctx context.Context, userID string, request KnowledgeRetrievalRequest) (KnowledgeRetrievalResult, error) {
	started := time.Now()
	query := strings.TrimSpace(request.Query)
	if query == "" {
		return KnowledgeRetrievalResult{}, ErrKnowledgeQueryRequired
	}
	if len([]rune(query)) > s.config.MaxQueryChars {
		return KnowledgeRetrievalResult{}, ErrKnowledgeQueryTooLong
	}
	baseIDs := uniqueStrings(request.KnowledgeBaseIDs)
	if len(baseIDs) == 0 {
		return KnowledgeRetrievalResult{}, ErrKnowledgeBasesRequired
	}
	if len(baseIDs) > s.config.MaxKnowledgeBases {
		return KnowledgeRetrievalResult{}, ErrTooManyKnowledgeBases
	}
	for _, baseID := range baseIDs {
		if err := uuid.Validate(baseID); err != nil {
			return KnowledgeRetrievalResult{}, ErrInvalidKnowledgeBaseID
		}
	}
	owned, err := s.repository.CountOwnedBases(ctx, userID, baseIDs)
	if err != nil {
		return KnowledgeRetrievalResult{}, err
	}
	if owned != int64(len(baseIDs)) {
		return KnowledgeRetrievalResult{}, ErrKnowledgeBaseNotFound
	}
	if s.embeddings == nil {
		return KnowledgeRetrievalResult{}, ErrEmbeddingNotConfigured
	}
	embedded, err := s.embeddings.EmbedQuery(ctx, embeddinggateway.Request{
		OperationID: "knowledge-query:" + uuid.NewString(), Purpose: "rag_query",
		UserID: userID, Texts: []string{query},
	})
	if err != nil {
		return KnowledgeRetrievalResult{}, fmt.Errorf("%w: %v", ErrKnowledgeRetrievalFailed, err)
	}
	if len(embedded.Vectors) != 1 || embedded.Dimension <= 0 || len(embedded.Vectors[0].Values) != embedded.Dimension || embedded.Provider == "" || embedded.Model == "" {
		return KnowledgeRetrievalResult{}, fmt.Errorf("%w: invalid query embedding result", ErrKnowledgeRetrievalFailed)
	}
	queryVector := pgvector.NewVector(embedded.Vectors[0].Values)
	candidates, err := s.repository.SearchChunksExact(ctx, userID, baseIDs, embedded.Provider, embedded.Model, embedded.Dimension, s.config.CandidateLimit, queryVector)
	if err != nil {
		return KnowledgeRetrievalResult{}, fmt.Errorf("%w: %v", ErrKnowledgeRetrievalFailed, err)
	}
	result := KnowledgeRetrievalResult{
		Matches: make([]KnowledgeRetrievalMatch, 0, s.config.TopK), CandidateCount: len(candidates),
		ContextTokenBudget: s.config.ContextTokenBudget, EmbeddingProvider: embedded.Provider,
		EmbeddingModel: embedded.Model, EmbeddingDimension: embedded.Dimension,
	}
	for _, candidate := range candidates {
		if math.IsNaN(candidate.Similarity) || math.IsInf(candidate.Similarity, 0) || candidate.Similarity < s.config.SimilarityThreshold {
			continue
		}
		contentTokens := candidate.TokenCount
		if contentTokens <= 0 {
			contentTokens = s.estimator.Estimate(candidate.Content)
		}
		overhead := s.estimator.Estimate(fmt.Sprintf("[来源：%s，第 %d 个片段]", candidate.DocumentName, candidate.ChunkIndex+1))
		contextTokens := contentTokens + overhead
		if contextTokens > s.config.ContextTokenBudget-result.UsedContextTokens {
			continue
		}
		metadata := make(map[string]any)
		if len(candidate.Metadata) > 0 {
			if unmarshalErr := json.Unmarshal(candidate.Metadata, &metadata); unmarshalErr != nil {
				metadata = nil
			}
		}
		result.UsedContextTokens += contextTokens
		result.Matches = append(result.Matches, KnowledgeRetrievalMatch{
			Rank: len(result.Matches) + 1, KnowledgeBaseID: candidate.KnowledgeBaseID,
			DocumentID: candidate.DocumentID, DocumentName: candidate.DocumentName,
			ChunkID: candidate.ChunkID, ChunkIndex: candidate.ChunkIndex,
			Content: candidate.Content, ContentHash: candidate.ContentHash,
			TokenCount: contentTokens, ContextTokens: contextTokens,
			PageNumber: candidate.PageNumber, SectionTitle: candidate.SectionTitle,
			Metadata: metadata, Similarity: candidate.Similarity,
		})
		if len(result.Matches) >= s.config.TopK {
			break
		}
	}
	logger.Info(ctx, "knowledge retrieval completed",
		logger.String("user_id", userID), logger.Int("knowledge_base_count", len(baseIDs)),
		logger.Int("candidate_count", len(candidates)), logger.Int("result_count", len(result.Matches)),
		logger.Int64("context_tokens", result.UsedContextTokens), logger.Int64("context_token_budget", s.config.ContextTokenBudget),
		logger.String("embedding_provider", embedded.Provider), logger.String("embedding_model", embedded.Model),
		logger.Int("embedding_dimension", embedded.Dimension), logger.Int64("duration_ms", time.Since(started).Milliseconds()))
	return result, nil
}
