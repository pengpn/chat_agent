package service

import (
	"context"
	"errors"
	"testing"

	"github.com/example/agent-platform/backend/internal/repository"
	"github.com/example/agent-platform/backend/internal/rerankgateway"
)

type fakeReranker struct {
	calls   int
	request rerankgateway.Request
	result  rerankgateway.Result
	err     error
}

func (f *fakeReranker) Rerank(_ context.Context, request rerankgateway.Request) (rerankgateway.Result, error) {
	f.calls++
	f.request = request
	return f.result, f.err
}

func TestKnowledgeRerankChangesOrderAndCitationButPreservesEvidenceGate(t *testing.T) {
	repo := newFakeKnowledgeRepository()
	repo.base.ID = retrievalTestBaseID
	repo.searchRows = []repository.KnowledgeSearchRow{
		{KnowledgeBaseID: retrievalTestBaseID, DocumentID: "generic", ChunkID: "a", Content: "一般说明", Similarity: 0.9, TokenCount: 3},
		{KnowledgeBaseID: retrievalTestBaseID, DocumentID: "refund", ChunkID: "b", Content: "退款期限七天", Similarity: 0.7, TokenCount: 5},
	}
	fake := &fakeReranker{result: rerankgateway.Result{Provider: "fake", Model: "rank-v1", Items: []rerankgateway.Item{{Index: 1, Score: 0.95}, {Index: 0, Score: 0.2}}}}
	svc := NewKnowledgeRetrieval(repo, &fakeEmbeddingGateway{}, retrievalFixedEstimator(1), KnowledgeRetrievalConfig{TopK: 1, SimilarityThreshold: 0.5, RerankEnabled: true})
	svc.ConfigureReranker(fake)
	request := KnowledgeRetrievalRequest{Query: "退款期限？", KnowledgeBaseIDs: []string{retrievalTestBaseID}}
	result, trace, err := svc.SearchWithTrace(context.Background(), "user-1", request)
	if err != nil || len(result.Matches) != 1 || result.Matches[0].ChunkID != "b" || trace.RerankModel != "rank-v1" || trace.Candidates[0].PreRerankRank != 2 || fake.calls != 1 || len(fake.request.Documents) != 2 {
		t.Fatalf("重排未在 Top-K 前生效: %+v,%+v,%v", result, trace, err)
	}
	_, citations := validatedCitations("七天[1]", result.Matches)
	if len(citations) != 1 || *citations[0].ChunkID != "b" {
		t.Fatal("引用编号必须对应重排后的选中来源")
	}
	repo.searchRows[1].Similarity = 0.1
	result, err = svc.Search(context.Background(), "user-1", request)
	if err != nil || result.Matches[0].ChunkID != "a" {
		t.Fatal("重排分数不能绕过原有相似度门槛")
	}
	fake.err = context.Canceled
	_, err = svc.Search(context.Background(), "user-1", request)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("取消未向上传播: %v", err)
	}
}

func TestKnowledgeRerankDisabledUnauthorizedAndInvalidResponse(t *testing.T) {
	repo := newFakeKnowledgeRepository()
	repo.base.ID = retrievalTestBaseID
	repo.searchRows = []repository.KnowledgeSearchRow{{ChunkID: "a", Content: "资料", Similarity: 0.9, TokenCount: 2}}
	fake := &fakeReranker{}
	svc := NewKnowledgeRetrieval(repo, &fakeEmbeddingGateway{}, retrievalFixedEstimator(1), KnowledgeRetrievalConfig{})
	svc.ConfigureReranker(fake)
	request := KnowledgeRetrievalRequest{Query: "问题", KnowledgeBaseIDs: []string{retrievalTestBaseID}}
	if _, err := svc.Search(context.Background(), "user-1", request); err != nil || fake.calls != 0 {
		t.Fatal("关闭时不应调用重排模型")
	}
	svc.config.RerankEnabled = true
	if _, err := svc.Search(context.Background(), "other-user", request); !errors.Is(err, ErrKnowledgeBaseNotFound) || fake.calls != 0 {
		t.Fatal("必须先验证权限")
	}
	fake.result = rerankgateway.Result{Items: []rerankgateway.Item{{Index: 9, Score: 1}}}
	if _, err := svc.Search(context.Background(), "user-1", request); !errors.Is(err, rerankgateway.ErrInvalidResponse) {
		t.Fatalf("越界索引必须报错而非映射错误来源: %v", err)
	}
}
