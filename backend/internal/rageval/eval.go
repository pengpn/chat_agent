// Package rageval 对已记录的 RAG 运行结果进行离线评估，不调用模型。
// 回答语义和事实断言是否正确，需要人工标注。
package rageval

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/example/agent-platform/backend/internal/embeddinggateway"
	"github.com/example/agent-platform/backend/internal/repository"
	"github.com/example/agent-platform/backend/internal/rerankgateway"
	"github.com/example/agent-platform/backend/internal/service"
)

const SchemaVersion = 1

// 相关性按文档标注为 1～3 级。这样即使切分实验改变了片段 ID，
// 不同实验仍能使用同一份文档级标准答案。
type Dataset struct {
	SchemaVersion int    `json:"schemaVersion"`
	ID            string `json:"id"`
	CorpusVersion string `json:"corpusVersion"`
	Cases         []Case `json:"cases"`
}

type Case struct {
	ID                          string                     `json:"id"`
	Query                       string                     `json:"query"`
	History                     []service.QueryRewriteTurn `json:"history,omitempty"`
	KnowledgeBaseIDs            []string                   `json:"knowledgeBaseIds,omitempty"`
	GoldAnswer                  string                     `json:"goldAnswer"`
	RelevantDocuments           map[string]int             `json:"relevantDocuments"`
	ExpectedCitationDocumentIDs []string                   `json:"expectedCitationDocumentIds"`
}

type Parameters struct {
	QueryRewrite         *QueryRewriteParameters `json:"queryRewrite,omitempty"`
	ChunkTargetTokens    int64                   `json:"chunkTargetTokens"`
	ChunkOverlapTokens   int64                   `json:"chunkOverlapTokens"`
	CandidateLimit       int                     `json:"candidateLimit"`
	TopK                 int                     `json:"topK"`
	SimilarityThreshold  float64                 `json:"similarityThreshold"`
	ContextTokenBudget   int64                   `json:"contextTokenBudget"`
	PromptVersion        string                  `json:"promptVersion"`
	RetrievalMode        string                  `json:"retrievalMode"`
	EmbeddingProvider    string                  `json:"embeddingProvider"`
	EmbeddingModel       string                  `json:"embeddingModel"`
	EmbeddingDimension   int                     `json:"embeddingDimension"`
	EmbeddingTokenizer   string                  `json:"embeddingTokenizer"`
	GenerationProvider   string                  `json:"generationProvider,omitempty"`
	GenerationModel      string                  `json:"generationModel,omitempty"`
	RRFK                 int                     `json:"rrfK,omitempty"`
	KeywordVersion       int                     `json:"keywordVersion,omitempty"`
	RerankEnabled        bool                    `json:"rerankEnabled,omitempty"`
	RerankProvider       string                  `json:"rerankProvider,omitempty"`
	RerankModel          string                  `json:"rerankModel,omitempty"`
	RerankTokenizer      string                  `json:"rerankTokenizer,omitempty"`
	RerankMaxDocuments   int                     `json:"rerankMaxDocuments,omitempty"`
	RerankMaxPairTokens  int64                   `json:"rerankMaxPairTokens,omitempty"`
	RerankMaxTotalTokens int64                   `json:"rerankMaxTotalTokens,omitempty"`
}

// QueryRewriteParameters 记录影响改写的全部服务端配置，避免只比较模型名字。
type QueryRewriteParameters struct {
	Provider           string `json:"provider"`
	Model              string `json:"model"`
	PromptVersion      string `json:"promptVersion"`
	MaxHistoryMessages int    `json:"maxHistoryMessages"`
	MaxHistoryChars    int    `json:"maxHistoryChars"`
	MaxInputTokens     int64  `json:"maxInputTokens"`
	MaxOutputTokens    int64  `json:"maxOutputTokens"`
	TimeoutMS          int64  `json:"timeoutMs"`
}

// Snapshot 保存一次实验。抓取命令先写入检索数据；复核人员再补充回答、
// 引用和事实断言标注，用于计算生成质量指标。
type Snapshot struct {
	SchemaVersion     int           `json:"schemaVersion"`
	DatasetID         string        `json:"datasetId"`
	CorpusVersion     string        `json:"corpusVersion"`
	CorpusFingerprint string        `json:"corpusFingerprint"`
	IndexFingerprint  string        `json:"indexFingerprint"`
	Name              string        `json:"name"`
	Parameters        Parameters    `json:"parameters"`
	Cases             []Observation `json:"cases"`
}

type Candidate struct {
	KnowledgeBaseID string   `json:"knowledgeBaseId"`
	DocumentID      string   `json:"documentId"`
	DocumentName    string   `json:"documentName"`
	ChunkID         string   `json:"chunkId"`
	ChunkIndex      int      `json:"chunkIndex"`
	Content         string   `json:"content"`
	ContentHash     string   `json:"contentHash"`
	TokenCount      int64    `json:"tokenCount"`
	Similarity      float64  `json:"similarity"`
	VectorRank      int      `json:"vectorRank,omitempty"`
	KeywordRank     int      `json:"keywordRank,omitempty"`
	KeywordScore    float64  `json:"keywordScore,omitempty"`
	FusionScore     float64  `json:"fusionScore,omitempty"`
	PreRerankRank   int      `json:"preRerankRank,omitempty"`
	RerankRank      int      `json:"rerankRank,omitempty"`
	RerankScore     *float64 `json:"rerankScore,omitempty"`
}

type Claim struct {
	Text                string   `json:"text"`
	Supported           bool     `json:"supported"`
	EvidenceDocumentIDs []string `json:"evidenceDocumentIds,omitempty"`
}

type Observation struct {
	RetrievalQuery          string                           `json:"retrievalQuery,omitempty"`
	HistoryHash             string                           `json:"historyHash,omitempty"`
	Rewrite                 *service.QueryRewriteObservation `json:"rewrite,omitempty"`
	CaseID                  string                           `json:"caseId"`
	Query                   string                           `json:"query"`
	Candidates              []Candidate                      `json:"candidates"`
	SelectedChunkIDs        []string                         `json:"selectedChunkIds,omitempty"`
	EmbeddingMS             int64                            `json:"embeddingMs"`
	SearchMS                int64                            `json:"searchMs"`
	KeywordSearchMS         int64                            `json:"keywordSearchMs"`
	TotalMS                 int64                            `json:"totalMs"`
	Answer                  string                           `json:"answer,omitempty"`
	AnswerCorrect           *bool                            `json:"answerCorrect,omitempty"`
	Claims                  []Claim                          `json:"claims,omitempty"`
	CitedDocumentIDs        []string                         `json:"citedDocumentIds,omitempty"`
	GenerationMS            int64                            `json:"generationMs,omitempty"`
	RerankMS                int64                            `json:"rerankMs"`
	RerankEstimatedTokens   int64                            `json:"rerankEstimatedTokens,omitempty"`
	RerankInputTokens       *int64                           `json:"rerankInputTokens,omitempty"`
	RerankBilledSearchUnits *float64                         `json:"rerankBilledSearchUnits,omitempty"`
}

type CaseResult struct {
	RetrievalQuery      string   `json:"retrievalQuery"`
	RewriteStatus       string   `json:"rewriteStatus"`
	RewriteMS           int64    `json:"rewriteMs"`
	CaseID              string   `json:"caseId"`
	CandidateChunkIDs   []string `json:"candidateChunkIds"`
	SelectedChunkIDs    []string `json:"selectedChunkIds"`
	SelectedDocumentIDs []string `json:"selectedDocumentIds"`
	CandidateRecall     float64  `json:"candidateRecall"`
	RecallAtK           float64  `json:"recallAtK"`
	MRRAtK              float64  `json:"mrrAtK"`
	NDCGAtK             float64  `json:"ndcgAtK"`
	Faithfulness        *float64 `json:"faithfulness,omitempty"`
	CitationPrecision   *float64 `json:"citationPrecision,omitempty"`
	CitationRecall      *float64 `json:"citationRecall,omitempty"`
	AnswerCorrect       *bool    `json:"answerCorrect,omitempty"`
	Outcome             string   `json:"outcome"`
	EmbeddingMS         int64    `json:"embeddingMs"`
	SearchMS            int64    `json:"searchMs"`
	KeywordSearchMS     int64    `json:"keywordSearchMs"`
	GenerationMS        int64    `json:"generationMs,omitempty"`
	RerankMS            int64    `json:"rerankMs"`
}

type Summary struct {
	MeanRewriteMS             float64      `json:"meanRewriteMs"`
	RewrittenCount            int          `json:"rewrittenCount"`
	RewriteFallbackCount      int          `json:"rewriteFallbackCount"`
	RewriteInputTokens        int64        `json:"rewriteInputTokens"`
	RewriteOutputTokens       int64        `json:"rewriteOutputTokens"`
	RewriteUsageReportedCases int          `json:"rewriteUsageReportedCases"`
	RewriteCostNanoUSD        int64        `json:"rewriteCostNanoUsd"`
	RewriteKnownCostCases     int          `json:"rewriteKnownCostCases"`
	Name                      string       `json:"name"`
	DatasetID                 string       `json:"datasetId"`
	CorpusFingerprint         string       `json:"corpusFingerprint"`
	IndexFingerprint          string       `json:"indexFingerprint"`
	Parameters                Parameters   `json:"parameters"`
	CaseCount                 int          `json:"caseCount"`
	AnswerableCount           int          `json:"answerableCount"`
	UnanswerableCount         int          `json:"unanswerableCount"`
	NoEvidenceAccuracy        *float64     `json:"noEvidenceAccuracy,omitempty"`
	GenerationGraded          int          `json:"generationGraded"`
	CandidateRecall           float64      `json:"candidateRecall"`
	RecallAtK                 float64      `json:"recallAtK"`
	MRRAtK                    float64      `json:"mrrAtK"`
	NDCGAtK                   float64      `json:"ndcgAtK"`
	Faithfulness              *float64     `json:"faithfulness,omitempty"`
	CitationPrecision         *float64     `json:"citationPrecision,omitempty"`
	CitationRecall            *float64     `json:"citationRecall,omitempty"`
	AnswerAccuracy            *float64     `json:"answerAccuracy,omitempty"`
	MeanEmbeddingMS           float64      `json:"meanEmbeddingMs"`
	MeanSearchMS              float64      `json:"meanSearchMs"`
	MeanKeywordSearchMS       float64      `json:"meanKeywordSearchMs"`
	MeanGenerationMS          *float64     `json:"meanGenerationMs,omitempty"`
	MeanRerankMS              float64      `json:"meanRerankMs"`
	Cases                     []CaseResult `json:"cases"`
}

func Evaluate(dataset Dataset, snapshot Snapshot) (Summary, error) {
	if err := ValidateDataset(dataset); err != nil {
		return Summary{}, err
	}
	if err := ValidateSnapshot(dataset, snapshot); err != nil {
		return Summary{}, err
	}
	estimator, err := embeddinggateway.NewTokenEstimator(snapshot.Parameters.EmbeddingTokenizer)
	if err != nil {
		return Summary{}, err
	}
	summary := Summary{
		Name: snapshot.Name, DatasetID: dataset.ID, CorpusFingerprint: snapshot.CorpusFingerprint,
		IndexFingerprint: snapshot.IndexFingerprint,
		Parameters:       snapshot.Parameters, CaseCount: len(dataset.Cases), Cases: make([]CaseResult, 0, len(dataset.Cases)),
	}
	byID := make(map[string]Observation, len(snapshot.Cases))
	for _, item := range snapshot.Cases {
		byID[item.CaseID] = item
	}
	var faith, precision, citationRecall, accuracy, generationMS float64
	var noEvidenceCorrect float64
	for _, gold := range dataset.Cases {
		observed := byID[gold.ID]
		result, err := evaluateCase(gold, observed, snapshot.Parameters, estimator)
		if err != nil {
			return Summary{}, err
		}
		summary.Cases = append(summary.Cases, result)
		if len(gold.RelevantDocuments) > 0 {
			summary.AnswerableCount++
			summary.CandidateRecall += result.CandidateRecall
			summary.RecallAtK += result.RecallAtK
			summary.MRRAtK += result.MRRAtK
			summary.NDCGAtK += result.NDCGAtK
		} else {
			summary.UnanswerableCount++
			if len(result.SelectedChunkIDs) == 0 {
				noEvidenceCorrect++
			}
		}
		summary.MeanEmbeddingMS += float64(result.EmbeddingMS)
		summary.MeanSearchMS += float64(result.SearchMS)
		summary.MeanKeywordSearchMS += float64(result.KeywordSearchMS)
		summary.MeanRerankMS += float64(result.RerankMS)
		summary.MeanRewriteMS += float64(result.RewriteMS)
		if result.RewriteStatus == "rewritten" {
			summary.RewrittenCount++
		}
		if result.RewriteStatus == "fallback" {
			summary.RewriteFallbackCount++
		}
		if observed.Rewrite != nil && observed.Rewrite.UsageReported {
			summary.RewriteUsageReportedCases++
			summary.RewriteInputTokens += observed.Rewrite.InputTokens
			summary.RewriteOutputTokens += observed.Rewrite.OutputTokens
			if observed.Rewrite.CostKnown {
				summary.RewriteKnownCostCases++
				summary.RewriteCostNanoUSD += observed.Rewrite.CostNanoUSD
			}
		}
		if result.Faithfulness != nil {
			summary.GenerationGraded++
			faith += *result.Faithfulness
			precision += *result.CitationPrecision
			citationRecall += *result.CitationRecall
			if *result.AnswerCorrect {
				accuracy++
			}
			generationMS += float64(result.GenerationMS)
		}
	}
	n := float64(len(dataset.Cases))
	if summary.AnswerableCount > 0 {
		a := float64(summary.AnswerableCount)
		summary.CandidateRecall /= a
		summary.RecallAtK /= a
		summary.MRRAtK /= a
		summary.NDCGAtK /= a
	}
	if summary.UnanswerableCount > 0 {
		summary.NoEvidenceAccuracy = ptr(noEvidenceCorrect / float64(summary.UnanswerableCount))
	}
	summary.MeanEmbeddingMS /= n
	summary.MeanSearchMS /= n
	summary.MeanKeywordSearchMS /= n
	summary.MeanRerankMS /= n
	summary.MeanRewriteMS /= n
	if summary.GenerationGraded > 0 {
		g := float64(summary.GenerationGraded)
		summary.Faithfulness = ptr(faith / g)
		summary.CitationPrecision = ptr(precision / g)
		summary.CitationRecall = ptr(citationRecall / g)
		summary.AnswerAccuracy = ptr(accuracy / g)
		summary.MeanGenerationMS = ptr(generationMS / g)
	}
	return summary, nil
}

func ValidateDataset(dataset Dataset) error {
	if dataset.SchemaVersion != SchemaVersion || dataset.ID == "" || dataset.CorpusVersion == "" || len(dataset.Cases) == 0 {
		return fmt.Errorf("invalid RAG evaluation dataset header")
	}
	seen := make(map[string]bool, len(dataset.Cases))
	for _, item := range dataset.Cases {
		if item.ID == "" || strings.TrimSpace(item.Query) == "" || strings.TrimSpace(item.GoldAnswer) == "" || seen[item.ID] {
			return fmt.Errorf("invalid or duplicate evaluation case %q", item.ID)
		}
		seen[item.ID] = true
		for _, turn := range item.History {
			if (turn.Role != "user" && turn.Role != "assistant") || strings.TrimSpace(turn.Content) == "" {
				return fmt.Errorf("case %q has invalid rewrite history", item.ID)
			}
		}
		for doc, grade := range item.RelevantDocuments {
			if doc == "" || grade < 1 || grade > 3 {
				return fmt.Errorf("case %q has invalid relevance grade", item.ID)
			}
		}
		for _, doc := range item.ExpectedCitationDocumentIDs {
			if item.RelevantDocuments[doc] == 0 {
				return fmt.Errorf("case %q expects a citation to a non-relevant document", item.ID)
			}
		}
	}
	return nil
}

func ValidateSnapshot(dataset Dataset, snapshot Snapshot) error {
	if snapshot.SchemaVersion != SchemaVersion || snapshot.DatasetID != dataset.ID || snapshot.CorpusVersion != dataset.CorpusVersion || snapshot.Name == "" || snapshot.CorpusFingerprint == "" || snapshot.IndexFingerprint == "" {
		return fmt.Errorf("snapshot header does not match dataset")
	}
	p := snapshot.Parameters
	if p.RetrievalMode != service.RetrievalModeVector && p.RetrievalMode != service.RetrievalModeHybrid {
		return fmt.Errorf("snapshot %q has unsupported retrieval mode", snapshot.Name)
	}
	if p.RetrievalMode == service.RetrievalModeHybrid && (p.RRFK <= 0 || p.RRFK > 10000 || p.KeywordVersion <= 0) {
		return fmt.Errorf("hybrid snapshot must record RRF constant and keyword version")
	}
	if p.RerankEnabled && (p.RerankProvider == "" || p.RerankModel == "" || p.RerankTokenizer == "" || p.RerankMaxDocuments <= 0 || p.RerankMaxDocuments > 400 || p.RerankMaxPairTokens <= 0 || p.RerankMaxTotalTokens <= 0) {
		return fmt.Errorf("reranked snapshot must record model and admission limits")
	}
	if rewrite := p.QueryRewrite; rewrite != nil {
		if rewrite.Provider == "" || rewrite.Model == "" || rewrite.PromptVersion == "" || rewrite.MaxHistoryMessages <= 0 || rewrite.MaxHistoryMessages > 50 || rewrite.MaxHistoryChars <= 0 || rewrite.MaxHistoryChars > 50000 || rewrite.MaxInputTokens <= 0 || rewrite.MaxOutputTokens <= 0 || rewrite.TimeoutMS <= 0 {
			return fmt.Errorf("query rewrite snapshot must record model, prompt and limits")
		}
	}
	if p.CandidateLimit <= 0 || p.CandidateLimit > 200 || p.TopK <= 0 || p.TopK > p.CandidateLimit || p.ContextTokenBudget <= 0 || math.IsNaN(p.SimilarityThreshold) || math.IsInf(p.SimilarityThreshold, 0) || p.SimilarityThreshold < 0 || p.SimilarityThreshold > 1 || p.ChunkTargetTokens <= 0 || p.ChunkOverlapTokens < 0 || p.ChunkOverlapTokens >= p.ChunkTargetTokens || p.PromptVersion == "" || p.RetrievalMode == "" || p.EmbeddingProvider == "" || p.EmbeddingModel == "" || p.EmbeddingDimension <= 0 || p.EmbeddingTokenizer == "" {
		return fmt.Errorf("snapshot %q has invalid experiment parameters", snapshot.Name)
	}
	if len(snapshot.Cases) != len(dataset.Cases) {
		return fmt.Errorf("snapshot %q has %d cases; dataset has %d", snapshot.Name, len(snapshot.Cases), len(dataset.Cases))
	}
	estimator, err := embeddinggateway.NewTokenEstimator(p.EmbeddingTokenizer)
	if err != nil {
		return err
	}
	gold := make(map[string]Case, len(dataset.Cases))
	for _, item := range dataset.Cases {
		gold[item.ID] = item
	}
	seen := make(map[string]bool, len(snapshot.Cases))
	for _, item := range snapshot.Cases {
		original, ok := gold[item.CaseID]
		maxCandidates := p.CandidateLimit
		if p.RetrievalMode == service.RetrievalModeHybrid {
			maxCandidates *= 2
		}
		if !ok || seen[item.CaseID] || item.Query != original.Query || len(item.Candidates) > maxCandidates {
			return fmt.Errorf("snapshot %q has invalid case %q", snapshot.Name, item.CaseID)
		}
		seen[item.CaseID] = true
		if err := validateRewriteObservation(original, item, p.QueryRewrite); err != nil {
			return fmt.Errorf("case %q: %w", item.CaseID, err)
		}
		if item.EmbeddingMS < 0 || item.SearchMS < 0 || item.KeywordSearchMS < 0 || item.TotalMS < 0 || item.GenerationMS < 0 {
			return fmt.Errorf("case %q has negative latency", item.CaseID)
		}
		if item.RerankMS < 0 || item.RerankEstimatedTokens < 0 || (item.RerankInputTokens != nil && *item.RerankInputTokens < 0) || (item.RerankBilledSearchUnits != nil && (*item.RerankBilledSearchUnits < 0 || math.IsNaN(*item.RerankBilledSearchUnits) || math.IsInf(*item.RerankBilledSearchUnits, 0))) {
			return fmt.Errorf("case %q has invalid rerank observation", item.CaseID)
		}
		if p.RerankEnabled && len(item.Candidates) > p.RerankMaxDocuments {
			return fmt.Errorf("reranked pool exceeds document limit")
		}
		chunkIDs := make(map[string]bool, len(item.Candidates))
		vectorRanks, keywordRanks := make(map[int]bool), make(map[int]bool)
		preRerankRanks, rerankRanks := make(map[int]bool), make(map[int]bool)
		for _, candidate := range item.Candidates {
			if candidate.ChunkID == "" || candidate.DocumentID == "" || candidate.KnowledgeBaseID == "" || chunkIDs[candidate.ChunkID] || math.IsNaN(candidate.Similarity) || math.IsInf(candidate.Similarity, 0) {
				return fmt.Errorf("case %q has invalid candidate", item.CaseID)
			}
			chunkIDs[candidate.ChunkID] = true
			if p.RerankEnabled {
				if candidate.RerankScore == nil || math.IsNaN(*candidate.RerankScore) || math.IsInf(*candidate.RerankScore, 0) || candidate.PreRerankRank <= 0 || candidate.PreRerankRank > len(item.Candidates) || candidate.RerankRank <= 0 || candidate.RerankRank > len(item.Candidates) || preRerankRanks[candidate.PreRerankRank] || rerankRanks[candidate.RerankRank] {
					return fmt.Errorf("case %q has invalid rerank permutation", item.CaseID)
				}
				preRerankRanks[candidate.PreRerankRank], rerankRanks[candidate.RerankRank] = true, true
			} else if candidate.RerankScore != nil || candidate.PreRerankRank != 0 || candidate.RerankRank != 0 {
				return fmt.Errorf("rerank fields present in a disabled experiment")
			}
			if p.RetrievalMode == service.RetrievalModeHybrid {
				if candidate.VectorRank < 0 || candidate.KeywordRank < 0 || candidate.VectorRank > p.CandidateLimit || candidate.KeywordRank > p.CandidateLimit || (candidate.VectorRank == 0 && candidate.KeywordRank == 0) || candidate.KeywordScore < 0 || math.IsNaN(candidate.KeywordScore) || math.IsInf(candidate.KeywordScore, 0) {
					return fmt.Errorf("case %q has invalid hybrid candidate ranks", item.CaseID)
				}
				if candidate.VectorRank > 0 {
					if vectorRanks[candidate.VectorRank] {
						return fmt.Errorf("duplicate vector rank")
					}
					vectorRanks[candidate.VectorRank] = true
				}
				if candidate.KeywordRank > 0 {
					if keywordRanks[candidate.KeywordRank] || candidate.KeywordScore <= 0 {
						return fmt.Errorf("invalid keyword rank or score")
					}
					keywordRanks[candidate.KeywordRank] = true
				}
			}
		}
		for i := 1; i <= len(vectorRanks); i++ {
			if !vectorRanks[i] {
				return fmt.Errorf("vector ranks must be contiguous")
			}
		}
		for i := 1; i <= len(keywordRanks); i++ {
			if !keywordRanks[i] {
				return fmt.Errorf("keyword ranks must be contiguous")
			}
		}
		selected, err := replayCandidates(item.Candidates, estimator, p)
		if err != nil {
			return err
		}
		if item.SelectedChunkIDs != nil {
			selectedIDs := make([]string, 0, len(selected.Matches))
			for _, match := range selected.Matches {
				selectedIDs = append(selectedIDs, match.ChunkID)
			}
			if !slices.Equal(item.SelectedChunkIDs, selectedIDs) {
				return fmt.Errorf("case %q recorded selection differs from replay", item.CaseID)
			}
		}
		if item.AnswerCorrect != nil {
			if strings.TrimSpace(item.Answer) == "" || len(item.Claims) == 0 || p.GenerationProvider == "" || p.GenerationModel == "" {
				return fmt.Errorf("case %q needs an answer and reviewed claims", item.CaseID)
			}
			_, validCitations := answerCitations(item.Answer, selected.Matches)
			if !sameStringSet(item.CitedDocumentIDs, validCitations) {
				return fmt.Errorf("case %q citation annotation differs from answer markers", item.CaseID)
			}
			for _, claim := range item.Claims {
				if claim.Text == "" || !strings.Contains(item.Answer, claim.Text) {
					return fmt.Errorf("case %q has a claim absent from the answer", item.CaseID)
				}
			}
		} else if item.Answer != "" || len(item.Claims) > 0 || len(item.CitedDocumentIDs) > 0 {
			return fmt.Errorf("case %q has generation data without an answerCorrect review", item.CaseID)
		}
	}
	return nil
}

func Compare(dataset Dataset, snapshots []Snapshot) ([]Summary, error) {
	if len(snapshots) == 0 {
		return nil, fmt.Errorf("at least one snapshot is required")
	}
	results := make([]Summary, 0, len(snapshots))
	seen := make(map[string]bool, len(snapshots))
	for _, snapshot := range snapshots {
		if snapshot.CorpusFingerprint != snapshots[0].CorpusFingerprint || seen[snapshot.Name] {
			return nil, fmt.Errorf("experiments must use the same corpus fingerprint and unique names")
		}
		seen[snapshot.Name] = true
		result, err := Evaluate(dataset, snapshot)
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, nil
}

func evaluateCase(gold Case, observed Observation, p Parameters, estimator embeddinggateway.TokenEstimator) (CaseResult, error) {
	allChunkIDs := make([]string, 0, len(observed.Candidates))
	candidateDocs := make(map[string]bool)
	for _, item := range observed.Candidates {
		allChunkIDs = append(allChunkIDs, item.ChunkID)
		candidateDocs[item.DocumentID] = true
	}
	selected, err := replayCandidates(observed.Candidates, estimator, p)
	if err != nil {
		return CaseResult{}, err
	}
	result := CaseResult{CaseID: gold.ID, CandidateChunkIDs: allChunkIDs, EmbeddingMS: observed.EmbeddingMS, SearchMS: observed.SearchMS, KeywordSearchMS: observed.KeywordSearchMS, GenerationMS: observed.GenerationMS, RerankMS: observed.RerankMS, RetrievalQuery: observed.RetrievalQuery, RewriteStatus: "disabled"}
	if result.RetrievalQuery == "" {
		result.RetrievalQuery = observed.Query
	}
	if observed.Rewrite != nil {
		result.RewriteMS, result.RewriteStatus = observed.Rewrite.LatencyMS, observed.Rewrite.Status
	}
	selectedDocs := make(map[string]bool)
	for _, match := range selected.Matches {
		result.SelectedChunkIDs = append(result.SelectedChunkIDs, match.ChunkID)
		if !selectedDocs[match.DocumentID] {
			result.SelectedDocumentIDs = append(result.SelectedDocumentIDs, match.DocumentID)
			selectedDocs[match.DocumentID] = true
		}
	}
	if len(gold.RelevantDocuments) == 0 {
		// 无答案问题只有在未选中任何来源时，才算检索正确。
		if len(selected.Matches) == 0 {
			result.CandidateRecall, result.RecallAtK, result.MRRAtK, result.NDCGAtK = 1, 1, 1, 1
		}
	} else {
		for doc := range gold.RelevantDocuments {
			if candidateDocs[doc] {
				result.CandidateRecall++
			}
			if selectedDocs[doc] {
				result.RecallAtK++
			}
		}
		result.CandidateRecall /= float64(len(gold.RelevantDocuments))
		result.RecallAtK /= float64(len(gold.RelevantDocuments))
		result.MRRAtK = reciprocalRank(result.SelectedDocumentIDs, gold.RelevantDocuments)
		result.NDCGAtK = ndcg(result.SelectedDocumentIDs, gold.RelevantDocuments, p.TopK)
	}
	switch {
	case len(gold.RelevantDocuments) == 0 && len(selected.Matches) > 0:
		result.Outcome = "no_evidence_false_positive"
	case result.CandidateRecall == 0:
		result.Outcome = "candidate_miss"
	case result.RecallAtK == 0:
		result.Outcome = "selection_miss"
	default:
		result.Outcome = "generation_ungraded"
	}
	if observed.AnswerCorrect != nil {
		result.AnswerCorrect = observed.AnswerCorrect
		supported := 0
		for _, claim := range observed.Claims {
			if claim.Supported {
				supported++
			}
		}
		result.Faithfulness = ptr(float64(supported) / float64(len(observed.Claims)))
		cited, _ := answerCitations(observed.Answer, selected.Matches)
		result.CitationPrecision, result.CitationRecall = citationScores(cited, gold.ExpectedCitationDocumentIDs, selectedDocs)
		if result.CandidateRecall > 0 && result.RecallAtK > 0 {
			if *observed.AnswerCorrect && *result.Faithfulness == 1 && *result.CitationPrecision == 1 && *result.CitationRecall == 1 {
				result.Outcome = "pass"
			} else {
				result.Outcome = "generation_or_citation_error"
			}
		}
	}
	return result, nil
}

func replayCandidates(candidates []Candidate, estimator embeddinggateway.TokenEstimator, p Parameters) (service.KnowledgeRetrievalResult, error) {
	rows := make([]repository.KnowledgeSearchRow, 0, len(candidates))
	for _, item := range candidates {
		rows = append(rows, repository.KnowledgeSearchRow{
			KnowledgeBaseID: item.KnowledgeBaseID, DocumentID: item.DocumentID, DocumentName: item.DocumentName,
			ChunkID: item.ChunkID, ChunkIndex: item.ChunkIndex, Content: item.Content,
			ContentHash: item.ContentHash, TokenCount: item.TokenCount, Similarity: item.Similarity,
			VectorRank: item.VectorRank, KeywordRank: item.KeywordRank, KeywordScore: item.KeywordScore, FusionScore: item.FusionScore,
			PreRerankRank: item.PreRerankRank, RerankRank: item.RerankRank,
		})
		if item.RerankScore != nil {
			rows[len(rows)-1].RerankScore = *item.RerankScore
		}
	}
	if p.RetrievalMode == service.RetrievalModeHybrid {
		var vector, keyword []repository.KnowledgeSearchRow
		for _, row := range rows {
			if row.VectorRank > 0 {
				vector = append(vector, row)
			}
			if row.KeywordRank > 0 {
				keyword = append(keyword, row)
			}
		}
		sort.Slice(vector, func(i, j int) bool { return vector[i].VectorRank < vector[j].VectorRank })
		sort.Slice(keyword, func(i, j int) bool { return keyword[i].KeywordRank < keyword[j].KeywordRank })
		rows = service.FuseKnowledgeCandidates(vector, keyword, p.RRFK)
	}
	if p.RerankEnabled && len(rows) > 0 {
		if p.RetrievalMode == service.RetrievalModeVector {
			sort.Slice(rows, func(i, j int) bool { return rows[i].PreRerankRank < rows[j].PreRerankRank })
		}
		items := make([]rerankgateway.Item, len(rows))
		originalRanks := make(map[string]int, len(rows))
		for i, row := range rows {
			if row.PreRerankRank != i+1 {
				return service.KnowledgeRetrievalResult{}, fmt.Errorf("recorded pre-rerank order differs from retrieval replay")
			}
			items[i] = rerankgateway.Item{Index: i, Score: row.RerankScore}
			originalRanks[row.ChunkID] = row.RerankRank
		}
		var err error
		rows, err = service.ApplyKnowledgeRerank(rows, items)
		if err != nil {
			return service.KnowledgeRetrievalResult{}, err
		}
		for _, row := range rows {
			if originalRanks[row.ChunkID] != row.RerankRank {
				return service.KnowledgeRetrievalResult{}, fmt.Errorf("recorded rerank order differs from score replay")
			}
		}
	}
	selected, _ := service.SelectKnowledgeCandidates(rows, estimator, service.KnowledgeRetrievalConfig{
		TopK: p.TopK, SimilarityThreshold: p.SimilarityThreshold, ContextTokenBudget: p.ContextTokenBudget,
		Mode: p.RetrievalMode, RRFK: p.RRFK,
	})
	return selected, nil
}

var citationMarkers = regexp.MustCompile(`\[(\d+)\]`)

// 未知引用编号会拉低引用精确率；有效编号按实际选中的来源映射到文档，
// 不直接信任手工填写的文档 ID。
func answerCitations(answer string, matches []service.KnowledgeRetrievalMatch) (all, valid []string) {
	for _, marker := range citationMarkers.FindAllStringSubmatch(answer, -1) {
		rank, err := strconv.Atoi(marker[1])
		if err != nil || rank <= 0 || rank > len(matches) {
			all = append(all, "invalid-marker:"+marker[0])
			continue
		}
		all = append(all, matches[rank-1].DocumentID)
		valid = append(valid, matches[rank-1].DocumentID)
	}
	return all, valid
}

func sameStringSet(left, right []string) bool {
	leftSet := make(map[string]bool, len(left))
	rightSet := make(map[string]bool, len(right))
	for _, value := range left {
		leftSet[value] = true
	}
	for _, value := range right {
		rightSet[value] = true
	}
	if len(leftSet) != len(rightSet) {
		return false
	}
	for value := range leftSet {
		if !rightSet[value] {
			return false
		}
	}
	return true
}

func reciprocalRank(ranked []string, relevance map[string]int) float64 {
	for i, doc := range ranked {
		if relevance[doc] > 0 {
			return 1 / float64(i+1)
		}
	}
	return 0
}

func ndcg(ranked []string, relevance map[string]int, k int) float64 {
	var actual float64
	for i, doc := range ranked {
		if i >= k {
			break
		}
		actual += (math.Pow(2, float64(relevance[doc])) - 1) / math.Log2(float64(i+2))
	}
	grades := make([]int, 0, len(relevance))
	for _, grade := range relevance {
		grades = append(grades, grade)
	}
	slices.SortFunc(grades, func(a, b int) int { return b - a })
	var ideal float64
	for i, grade := range grades {
		if i >= k {
			break
		}
		ideal += (math.Pow(2, float64(grade)) - 1) / math.Log2(float64(i+2))
	}
	if ideal == 0 {
		return 0
	}
	return actual / ideal
}

func citationScores(cited, expected []string, selected map[string]bool) (*float64, *float64) {
	want := make(map[string]bool, len(expected))
	for _, doc := range expected {
		want[doc] = true
	}
	unique := make(map[string]bool, len(cited))
	correct := 0
	for _, doc := range cited {
		if unique[doc] {
			continue
		}
		unique[doc] = true
		if want[doc] && selected[doc] {
			correct++
		}
	}
	precision, recall := 0.0, 0.0
	if len(unique) == 0 && len(want) == 0 {
		precision, recall = 1, 1
	} else {
		if len(unique) > 0 {
			precision = float64(correct) / float64(len(unique))
		}
		if len(want) > 0 {
			recall = float64(correct) / float64(len(want))
		}
	}
	return &precision, &recall
}

func ptr[T any](value T) *T { return &value }

// FingerprintSourceDocuments 对输入排序后计算指纹，避免遍历顺序影响结果。
// 是否随重新索引变化，取决于调用方传入的是源文档标识还是索引版本信息。
func FingerprintSourceDocuments(entries []string) string {
	sorted := slices.Clone(entries)
	slices.Sort(sorted)
	digest := sha256.Sum256([]byte(strings.Join(sorted, "\n")))
	return hex.EncodeToString(digest[:])
}

func DecodeDataset(data []byte) (Dataset, error) {
	var dataset Dataset
	if err := json.Unmarshal(data, &dataset); err != nil {
		return Dataset{}, err
	}
	return dataset, ValidateDataset(dataset)
}
