package service

import (
	"context"
	"math"
	"testing"

	"github.com/example/agent-platform/backend/internal/repository"
)

func TestRRFUsesRanksDeduplicatesAndBreaksTiesStably(t *testing.T) {
	vector := []repository.KnowledgeSearchRow{
		{ChunkID: "a", DocumentID: "doc-a", Similarity: 0.99},
		{ChunkID: "b", DocumentID: "doc-b", Similarity: 0.6},
	}
	keyword := []repository.KnowledgeSearchRow{
		{ChunkID: "b", DocumentID: "doc-b", Similarity: 0.6, KeywordScore: 0.01},
		{ChunkID: "c", DocumentID: "doc-c", Similarity: 0.1, KeywordScore: 0.001},
		{ChunkID: "b", DocumentID: "doc-b", Similarity: 0.6, KeywordScore: 999},
	}
	got := FuseKnowledgeCandidates(vector, keyword, 60)
	if len(got) != 3 || got[0].ChunkID != "b" || got[0].VectorRank != 2 || got[0].KeywordRank != 1 {
		t.Fatalf("融合排序或去重错误: %+v", got)
	}
	if math.Abs(got[0].FusionScore-(1.0/62+1.0/61)) > 1e-12 || got[0].Similarity != 0.6 {
		t.Fatalf("RRF 不能混入原始分数: %+v", got[0])
	}
	tied := FuseKnowledgeCandidates(vector[:1], []repository.KnowledgeSearchRow{{ChunkID: "z", DocumentID: "doc-0", KeywordScore: 0.1}}, 60)
	if tied[0].ChunkID != "z" {
		t.Fatalf("同分应按来源稳定排序: %+v", tied)
	}
}

func TestHybridRetrievalRescuesKeywordOnlyCandidateBelowCosineThreshold(t *testing.T) {
	repo := newFakeKnowledgeRepository()
	repo.base.ID = retrievalTestBaseID
	repo.searchRows = []repository.KnowledgeSearchRow{{KnowledgeBaseID: retrievalTestBaseID, DocumentID: "doc-generic", DocumentName: "常见问题", ChunkID: "generic", Content: "一般说明", TokenCount: 3, Similarity: 0.8}}
	repo.keywordRows = []repository.KnowledgeSearchRow{{KnowledgeBaseID: retrievalTestBaseID, DocumentID: "doc-contract", DocumentName: "条款", ChunkID: "contract", Content: "SLA-2026 的响应时间为十分钟", TokenCount: 8, Similarity: 0.1, KeywordScore: 0.03}}
	request := KnowledgeRetrievalRequest{Query: "SLA-2026 的响应时间？", KnowledgeBaseIDs: []string{retrievalTestBaseID}}
	vector := NewKnowledgeRetrieval(repo, &fakeEmbeddingGateway{}, retrievalFixedEstimator(1), KnowledgeRetrievalConfig{TopK: 3, SimilarityThreshold: 0.5})
	baseline, err := vector.Search(context.Background(), "user-1", request)
	if err != nil || len(baseline.Matches) != 1 || repo.keywordCalls != 0 {
		t.Fatalf("默认模式不应执行关键词查询: %+v, %v", baseline, err)
	}
	hybrid := NewKnowledgeRetrieval(repo, &fakeEmbeddingGateway{}, retrievalFixedEstimator(1), KnowledgeRetrievalConfig{Mode: RetrievalModeHybrid, RRFK: 60, TopK: 3, SimilarityThreshold: 0.5})
	result, trace, err := hybrid.SearchWithTrace(context.Background(), "user-1", request)
	if err != nil || len(result.Matches) != 2 || trace.VectorCandidateCount != 1 || trace.KeywordCandidateCount != 1 {
		t.Fatalf("混合召回结果错误: %+v, %+v, %v", result, trace, err)
	}
	found := false
	for _, match := range result.Matches {
		if match.ChunkID == "contract" && match.Similarity == 0.1 && match.KeywordRank == 1 && match.FusionScore > 0 {
			found = true
		}
	}
	if !found {
		t.Fatal("关键词单路命中的片段被余弦阈值误删")
	}
}
