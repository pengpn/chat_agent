package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/example/agent-platform/backend/internal/database/model"
	"github.com/example/agent-platform/backend/internal/repository"
)

type retrievalFixedEstimator int64

const retrievalTestBaseID = "11111111-1111-1111-1111-111111111111"

func (e retrievalFixedEstimator) Estimate(string) int64 { return int64(e) }

func TestKnowledgeRetrievalAppliesThresholdTopKAndContextBudget(t *testing.T) {
	repositoryFake := newFakeKnowledgeRepository()
	repositoryFake.base.ID = retrievalTestBaseID
	metadata, _ := json.Marshal(map[string]any{"startChar": 10, "endChar": 20})
	repositoryFake.searchRows = []repository.KnowledgeSearchRow{
		{KnowledgeBaseID: retrievalTestBaseID, DocumentID: "doc-1", DocumentName: "规则.txt", ChunkID: "chunk-1", ChunkIndex: 0, Content: "第一条", TokenCount: 4, Metadata: model.JSONDocument(metadata), Similarity: 0.90},
		{KnowledgeBaseID: retrievalTestBaseID, DocumentID: "doc-1", DocumentName: "规则.txt", ChunkID: "chunk-2", ChunkIndex: 1, Content: "过大的片段", TokenCount: 20, Similarity: 0.80},
		{KnowledgeBaseID: retrievalTestBaseID, DocumentID: "doc-2", DocumentName: "手册.pdf", ChunkID: "chunk-3", ChunkIndex: 0, Content: "第三条", TokenCount: 4, Similarity: 0.70},
		{KnowledgeBaseID: retrievalTestBaseID, DocumentID: "doc-2", DocumentName: "手册.pdf", ChunkID: "chunk-4", ChunkIndex: 1, Content: "低相关", TokenCount: 4, Similarity: 0.20},
	}
	embeddings := &fakeEmbeddingGateway{}
	service := NewKnowledgeRetrieval(repositoryFake, embeddings, retrievalFixedEstimator(2), KnowledgeRetrievalConfig{
		CandidateLimit: 10, TopK: 3, SimilarityThreshold: 0.5,
		ContextTokenBudget: 12, MaxKnowledgeBases: 2, MaxQueryChars: 100,
	})
	result, err := service.Search(context.Background(), "user-1", KnowledgeRetrievalRequest{
		Query: "退款规则是什么？", KnowledgeBaseIDs: []string{retrievalTestBaseID, retrievalTestBaseID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 2 || result.Matches[0].ChunkID != "chunk-1" || result.Matches[1].ChunkID != "chunk-3" {
		t.Fatalf("unexpected matches: %+v", result.Matches)
	}
	if result.Matches[0].Rank != 1 || result.Matches[1].Rank != 2 || result.UsedContextTokens != 12 {
		t.Fatalf("unexpected ranks or budget: %+v", result)
	}
	if result.Matches[0].Metadata["startChar"] != float64(10) {
		t.Fatalf("metadata was not decoded: %+v", result.Matches[0].Metadata)
	}
	if repositoryFake.lastSearch.provider != "test" || repositoryFake.lastSearch.model != "embedding-v1" || repositoryFake.lastSearch.dimension != 3 || repositoryFake.lastSearch.limit != 10 {
		t.Fatalf("query did not use current embedding version: %+v", repositoryFake.lastSearch)
	}
}

func TestKnowledgeRetrievalRejectsUnauthorizedBaseBeforeEmbedding(t *testing.T) {
	repositoryFake := newFakeKnowledgeRepository()
	repositoryFake.base.ID = retrievalTestBaseID
	embeddings := &fakeEmbeddingGateway{}
	service := NewKnowledgeRetrieval(repositoryFake, embeddings, nil, KnowledgeRetrievalConfig{Mode: RetrievalModeHybrid})
	_, err := service.Search(context.Background(), "user-1", KnowledgeRetrievalRequest{Query: "问题", KnowledgeBaseIDs: []string{"22222222-2222-2222-2222-222222222222"}})
	if !errors.Is(err, ErrKnowledgeBaseNotFound) {
		t.Fatalf("error = %v, want ErrKnowledgeBaseNotFound", err)
	}
	embeddings.mu.Lock()
	queryCalls := embeddings.queryCalls
	embeddings.mu.Unlock()
	if queryCalls != 0 {
		t.Fatalf("embedding was called before ownership validation: %d", queryCalls)
	}
	if repositoryFake.keywordCalls != 0 {
		t.Fatal("关键词查询不应在权限校验之前执行")
	}
}

func TestKnowledgeRetrievalReturnsEmptyInsteadOfFillingBelowThreshold(t *testing.T) {
	repositoryFake := newFakeKnowledgeRepository()
	repositoryFake.base.ID = retrievalTestBaseID
	repositoryFake.searchRows = []repository.KnowledgeSearchRow{{ChunkID: "low", TokenCount: 1, Similarity: 0.1}}
	service := NewKnowledgeRetrieval(repositoryFake, &fakeEmbeddingGateway{}, retrievalFixedEstimator(1), KnowledgeRetrievalConfig{SimilarityThreshold: 0.8})
	result, err := service.Search(context.Background(), "user-1", KnowledgeRetrievalRequest{Query: "没有答案的问题", KnowledgeBaseIDs: []string{retrievalTestBaseID}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Matches == nil || len(result.Matches) != 0 {
		t.Fatalf("expected a non-nil empty result, got %+v", result.Matches)
	}
}

func TestKnowledgeRetrievalRejectsMalformedBaseID(t *testing.T) {
	service := NewKnowledgeRetrieval(newFakeKnowledgeRepository(), &fakeEmbeddingGateway{}, nil, KnowledgeRetrievalConfig{})
	_, err := service.Search(context.Background(), "user-1", KnowledgeRetrievalRequest{Query: "问题", KnowledgeBaseIDs: []string{"not-a-uuid"}})
	if !errors.Is(err, ErrInvalidKnowledgeBaseID) {
		t.Fatalf("error = %v, want ErrInvalidKnowledgeBaseID", err)
	}
}

func TestKnowledgeRetrievalTraceRecordsCandidateDecisions(t *testing.T) {
	repo := newFakeKnowledgeRepository()
	repo.base.ID = retrievalTestBaseID
	repo.searchRows = []repository.KnowledgeSearchRow{
		{KnowledgeBaseID: retrievalTestBaseID, DocumentID: "doc-1", DocumentName: "规则", ChunkID: "chunk-1", Content: "证据", TokenCount: 2, Similarity: 0.9},
		{KnowledgeBaseID: retrievalTestBaseID, DocumentID: "doc-2", DocumentName: "规则", ChunkID: "chunk-2", Content: "弱相关", TokenCount: 2, Similarity: 0.2},
		{KnowledgeBaseID: retrievalTestBaseID, DocumentID: "doc-3", DocumentName: "规则", ChunkID: "chunk-3", Content: "长证据", TokenCount: 20, Similarity: 0.8},
	}
	svc := NewKnowledgeRetrieval(repo, &fakeEmbeddingGateway{}, retrievalFixedEstimator(1), KnowledgeRetrievalConfig{
		CandidateLimit: 3, TopK: 2, SimilarityThreshold: 0.5, ContextTokenBudget: 5,
	})
	_, trace, err := svc.SearchWithTrace(context.Background(), "user-1", KnowledgeRetrievalRequest{
		Query: "规则是什么？", KnowledgeBaseIDs: []string{retrievalTestBaseID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if trace.Query != "规则是什么？" || trace.EmbeddingModel != "embedding-v1" || trace.PromptVersion != groundingPromptVersion || len(trace.Candidates) != 3 {
		t.Fatalf("incomplete trace: %+v", trace)
	}
	if trace.Candidates[0].Decision != "selected" || trace.Candidates[1].Decision != "below_threshold" || trace.Candidates[2].Decision != "over_budget" {
		t.Fatalf("unexpected candidate decisions: %+v", trace.Candidates)
	}
	if len(trace.SelectedChunkIDs) != 1 || trace.SelectedChunkIDs[0] != "chunk-1" || trace.EmbeddingMS < 0 || trace.SearchMS < 0 {
		t.Fatalf("unexpected selection or latency: %+v", trace)
	}
}
