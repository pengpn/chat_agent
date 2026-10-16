package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/example/agent-platform/backend/internal/embeddinggateway"
	"github.com/example/agent-platform/backend/internal/knowledgelex"
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
	TraceEnabled        bool
	Mode                string
	RRFK                int
}

// KnowledgeRetrievalTrace 是检索诊断快照，可能包含用户文档原文。
// 调用方只能将其保存在有访问控制的位置。
type KnowledgeRetrievalTrace struct {
	Query                 string                    `json:"query"`
	KnowledgeBaseIDs      []string                  `json:"knowledgeBaseIds"`
	Candidates            []KnowledgeCandidateTrace `json:"candidates"`
	SelectedChunkIDs      []string                  `json:"selectedChunkIds"`
	EmbeddingProvider     string                    `json:"embeddingProvider"`
	EmbeddingModel        string                    `json:"embeddingModel"`
	EmbeddingDimension    int                       `json:"embeddingDimension"`
	PromptVersion         string                    `json:"promptVersion"`
	CandidateLimit        int                       `json:"candidateLimit"`
	TopK                  int                       `json:"topK"`
	Threshold             float64                   `json:"threshold"`
	ContextTokenBudget    int64                     `json:"contextTokenBudget"`
	EmbeddingMS           int64                     `json:"embeddingMs"`
	SearchMS              int64                     `json:"searchMs"`
	TotalMS               int64                     `json:"totalMs"`
	RetrievalMode         string                    `json:"retrievalMode"`
	RRFK                  int                       `json:"rrfK,omitempty"`
	VectorCandidateCount  int                       `json:"vectorCandidateCount"`
	KeywordCandidateCount int                       `json:"keywordCandidateCount"`
	KeywordTerms          []string                  `json:"keywordTerms,omitempty"`
	KeywordSearchMS       int64                     `json:"keywordSearchMs"`
}

type KnowledgeCandidateTrace struct {
	Rank            int     `json:"rank"`
	KnowledgeBaseID string  `json:"knowledgeBaseId"`
	DocumentID      string  `json:"documentId"`
	DocumentName    string  `json:"documentName"`
	ChunkID         string  `json:"chunkId"`
	ChunkIndex      int     `json:"chunkIndex"`
	Content         string  `json:"content"`
	ContentHash     string  `json:"contentHash"`
	TokenCount      int64   `json:"tokenCount"`
	Similarity      float64 `json:"similarity"`
	Decision        string  `json:"decision"`
	VectorRank      int     `json:"vectorRank,omitempty"`
	KeywordRank     int     `json:"keywordRank,omitempty"`
	KeywordScore    float64 `json:"keywordScore,omitempty"`
	FusionScore     float64 `json:"fusionScore,omitempty"`
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
	VectorRank      int            `json:"vectorRank,omitempty"`
	KeywordRank     int            `json:"keywordRank,omitempty"`
	KeywordScore    float64        `json:"keywordScore,omitempty"`
	FusionScore     float64        `json:"fusionScore,omitempty"`
}

type KnowledgeRetrievalResult struct {
	Matches            []KnowledgeRetrievalMatch `json:"matches"`
	CandidateCount     int                       `json:"candidateCount"`
	UsedContextTokens  int64                     `json:"usedContextTokens"`
	ContextTokenBudget int64                     `json:"contextTokenBudget"`
	EmbeddingProvider  string                    `json:"embeddingProvider"`
	EmbeddingModel     string                    `json:"embeddingModel"`
	EmbeddingDimension int                       `json:"embeddingDimension"`
	RetrievalMode      string                    `json:"retrievalMode"`
}

// KnowledgeRetrievalService 与文档摄取分开，便于独立评估检索质量，
// 不必将检索与对话生成绑定。
type KnowledgeRetrievalService struct {
	repository repository.KnowledgeRepository
	embeddings embeddinggateway.Gateway
	estimator  chunkTokenEstimator
	config     KnowledgeRetrievalConfig
}

func NewKnowledgeRetrieval(repository repository.KnowledgeRepository, embeddings embeddinggateway.Gateway, estimator chunkTokenEstimator, cfg KnowledgeRetrievalConfig) *KnowledgeRetrievalService {
	if cfg.Mode == "" {
		cfg.Mode = RetrievalModeVector
	}
	if cfg.RRFK <= 0 {
		cfg.RRFK = 60
	}
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
	result, _, err := s.search(ctx, userID, request, s.config.TraceEnabled)
	return result, err
}

// SearchWithTrace 复用线上检索路径，返回每个候选片段的筛选结果供离线评估。
// 诊断快照不会暴露给 HTTP 接口。
func (s *KnowledgeRetrievalService) SearchWithTrace(ctx context.Context, userID string, request KnowledgeRetrievalRequest) (KnowledgeRetrievalResult, KnowledgeRetrievalTrace, error) {
	return s.search(ctx, userID, request, true)
}

func (s *KnowledgeRetrievalService) search(ctx context.Context, userID string, request KnowledgeRetrievalRequest, captureTrace bool) (KnowledgeRetrievalResult, KnowledgeRetrievalTrace, error) {
	started := time.Now()
	query := strings.TrimSpace(request.Query)
	if query == "" {
		return KnowledgeRetrievalResult{}, KnowledgeRetrievalTrace{}, ErrKnowledgeQueryRequired
	}
	if len([]rune(query)) > s.config.MaxQueryChars {
		return KnowledgeRetrievalResult{}, KnowledgeRetrievalTrace{}, ErrKnowledgeQueryTooLong
	}
	baseIDs := uniqueStrings(request.KnowledgeBaseIDs)
	if len(baseIDs) == 0 {
		return KnowledgeRetrievalResult{}, KnowledgeRetrievalTrace{}, ErrKnowledgeBasesRequired
	}
	if len(baseIDs) > s.config.MaxKnowledgeBases {
		return KnowledgeRetrievalResult{}, KnowledgeRetrievalTrace{}, ErrTooManyKnowledgeBases
	}
	for _, baseID := range baseIDs {
		if err := uuid.Validate(baseID); err != nil {
			return KnowledgeRetrievalResult{}, KnowledgeRetrievalTrace{}, ErrInvalidKnowledgeBaseID
		}
	}
	owned, err := s.repository.CountOwnedBases(ctx, userID, baseIDs)
	if err != nil {
		return KnowledgeRetrievalResult{}, KnowledgeRetrievalTrace{}, err
	}
	if owned != int64(len(baseIDs)) {
		return KnowledgeRetrievalResult{}, KnowledgeRetrievalTrace{}, ErrKnowledgeBaseNotFound
	}
	if s.embeddings == nil {
		return KnowledgeRetrievalResult{}, KnowledgeRetrievalTrace{}, ErrEmbeddingNotConfigured
	}
	embeddingStarted := time.Now()
	embedded, err := s.embeddings.EmbedQuery(ctx, embeddinggateway.Request{
		OperationID: "knowledge-query:" + uuid.NewString(), Purpose: "rag_query",
		UserID: userID, Texts: []string{query},
	})
	if err != nil {
		return KnowledgeRetrievalResult{}, KnowledgeRetrievalTrace{}, fmt.Errorf("%w: %v", ErrKnowledgeRetrievalFailed, err)
	}
	embeddingMS := time.Since(embeddingStarted).Milliseconds()
	if len(embedded.Vectors) != 1 || embedded.Dimension <= 0 || len(embedded.Vectors[0].Values) != embedded.Dimension || embedded.Provider == "" || embedded.Model == "" {
		return KnowledgeRetrievalResult{}, KnowledgeRetrievalTrace{}, fmt.Errorf("%w: invalid query embedding result", ErrKnowledgeRetrievalFailed)
	}
	queryVector := pgvector.NewVector(embedded.Vectors[0].Values)
	searchStarted := time.Now()
	candidates, err := s.repository.SearchChunksExact(ctx, userID, baseIDs, embedded.Provider, embedded.Model, embedded.Dimension, s.config.CandidateLimit, queryVector)
	if err != nil {
		return KnowledgeRetrievalResult{}, KnowledgeRetrievalTrace{}, fmt.Errorf("%w: %v", ErrKnowledgeRetrievalFailed, err)
	}
	searchMS := time.Since(searchStarted).Milliseconds()
	vectorCandidateCount := len(candidates)
	keywordCandidateCount := 0
	keywordSearchMS := int64(0)
	if s.config.Mode == RetrievalModeHybrid {
		keywordStarted := time.Now()
		keyword, keywordErr := s.repository.SearchChunksKeyword(ctx, userID, baseIDs, embedded.Provider, embedded.Model, embedded.Dimension, s.config.CandidateLimit, query, queryVector)
		if keywordErr != nil {
			return KnowledgeRetrievalResult{}, KnowledgeRetrievalTrace{}, fmt.Errorf("%w: keyword search: %v", ErrKnowledgeRetrievalFailed, keywordErr)
		}
		keywordCandidateCount = len(keyword)
		keywordSearchMS = time.Since(keywordStarted).Milliseconds()
		candidates = FuseKnowledgeCandidates(candidates, keyword, s.config.RRFK)
	}
	result, decisions := SelectKnowledgeCandidates(candidates, s.estimator, s.config)
	result.EmbeddingProvider, result.EmbeddingModel, result.EmbeddingDimension = embedded.Provider, embedded.Model, embedded.Dimension
	result.RetrievalMode = s.config.Mode
	trace := KnowledgeRetrievalTrace{}
	if captureTrace {
		trace = KnowledgeRetrievalTrace{
			Query: query, KnowledgeBaseIDs: baseIDs, Candidates: decisions,
			EmbeddingProvider: embedded.Provider, EmbeddingModel: embedded.Model, EmbeddingDimension: embedded.Dimension,
			PromptVersion: groundingPromptVersion, CandidateLimit: s.config.CandidateLimit, TopK: s.config.TopK,
			Threshold: s.config.SimilarityThreshold, ContextTokenBudget: s.config.ContextTokenBudget,
			EmbeddingMS: embeddingMS, SearchMS: searchMS, TotalMS: time.Since(started).Milliseconds(),
			RetrievalMode: s.config.Mode, RRFK: s.config.RRFK,
			VectorCandidateCount: vectorCandidateCount, KeywordCandidateCount: keywordCandidateCount,
			KeywordSearchMS: keywordSearchMS,
		}
		if s.config.Mode == RetrievalModeHybrid {
			trace.KeywordTerms = knowledgelex.QueryTerms(query)
		}
		for _, match := range result.Matches {
			trace.SelectedChunkIDs = append(trace.SelectedChunkIDs, match.ChunkID)
		}
		if s.config.TraceEnabled {
			logger.Info(ctx, "knowledge retrieval trace", logger.String("user_id", userID),
				logger.Any("trace", trace))
		}
	}
	logger.Info(ctx, "knowledge retrieval completed",
		logger.String("user_id", userID), logger.Int("knowledge_base_count", len(baseIDs)),
		logger.Int("candidate_count", len(candidates)), logger.Int("result_count", len(result.Matches)),
		logger.String("retrieval_mode", s.config.Mode), logger.Int("vector_candidate_count", vectorCandidateCount),
		logger.Int("keyword_candidate_count", keywordCandidateCount), logger.Int64("keyword_search_ms", keywordSearchMS),
		logger.Int64("context_tokens", result.UsedContextTokens), logger.Int64("context_token_budget", s.config.ContextTokenBudget),
		logger.String("embedding_provider", embedded.Provider), logger.String("embedding_model", embedded.Model),
		logger.Int("embedding_dimension", embedded.Dimension), logger.Int64("duration_ms", time.Since(started).Milliseconds()))
	return result, trace, nil
}

// SelectKnowledgeCandidates 由线上检索和离线重放共用。
// 传入的候选片段必须已按余弦相似度或 RRF 融合结果排好序。
func SelectKnowledgeCandidates(candidates []repository.KnowledgeSearchRow, estimator embeddinggateway.TokenEstimator, cfg KnowledgeRetrievalConfig) (KnowledgeRetrievalResult, []KnowledgeCandidateTrace) {
	if estimator == nil {
		estimator = embeddinggateway.HeuristicTokenEstimator{}
	}
	result := KnowledgeRetrievalResult{Matches: make([]KnowledgeRetrievalMatch, 0), CandidateCount: len(candidates), ContextTokenBudget: cfg.ContextTokenBudget}
	decisions := make([]KnowledgeCandidateTrace, 0, len(candidates))
	for index, candidate := range candidates {
		decision := KnowledgeCandidateTrace{
			Rank: index + 1, KnowledgeBaseID: candidate.KnowledgeBaseID, DocumentID: candidate.DocumentID,
			DocumentName: candidate.DocumentName, ChunkID: candidate.ChunkID, ChunkIndex: candidate.ChunkIndex,
			Content: candidate.Content, ContentHash: candidate.ContentHash, TokenCount: candidate.TokenCount,
			Similarity: candidate.Similarity,
			VectorRank: candidate.VectorRank, KeywordRank: candidate.KeywordRank,
			KeywordScore: candidate.KeywordScore, FusionScore: candidate.FusionScore,
		}
		switch {
		case len(result.Matches) >= cfg.TopK:
			decision.Decision = "top_k_reached"
		case math.IsNaN(candidate.Similarity) || math.IsInf(candidate.Similarity, 0):
			decision.Decision = "invalid_score"
		case candidate.Similarity < cfg.SimilarityThreshold && !(cfg.Mode == RetrievalModeHybrid && candidate.KeywordRank > 0 && candidate.KeywordScore > 0):
			decision.Decision = "below_threshold"
		default:
			contentTokens := candidate.TokenCount
			if contentTokens <= 0 {
				contentTokens = estimator.Estimate(candidate.Content)
			}
			overhead := estimator.Estimate(fmt.Sprintf("[来源：%s，第 %d 个片段]", candidate.DocumentName, candidate.ChunkIndex+1))
			contextTokens := contentTokens + overhead
			if contextTokens > cfg.ContextTokenBudget-result.UsedContextTokens {
				decision.Decision = "over_budget"
				break
			}
			metadata := make(map[string]any)
			if len(candidate.Metadata) > 0 {
				if err := json.Unmarshal(candidate.Metadata, &metadata); err != nil {
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
				VectorRank: candidate.VectorRank, KeywordRank: candidate.KeywordRank,
				KeywordScore: candidate.KeywordScore, FusionScore: candidate.FusionScore,
			})
			decision.Decision = "selected"
		}
		decisions = append(decisions, decision)
	}
	return result, decisions
}
