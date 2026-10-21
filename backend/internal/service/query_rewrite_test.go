package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/example/agent-platform/backend/internal/config"
	"github.com/example/agent-platform/backend/internal/database/model"
	"github.com/example/agent-platform/backend/internal/llmgateway"
	"github.com/example/agent-platform/backend/internal/provider"
	"github.com/example/agent-platform/backend/internal/repository"
	"github.com/example/agent-platform/backend/internal/rerankgateway"
)

// 测试仅替换改写使用的网关方法，其他方法若被误调用会立即失败。
type rewriteTestGateway struct {
	llmgateway.Gateway
	requests []llmgateway.Request
	chat     func(context.Context, llmgateway.Request) (provider.ChatResponse, error)
	estimate func(provider.ChatRequest) int64
}

func (g *rewriteTestGateway) Chat(ctx context.Context, request llmgateway.Request) (provider.ChatResponse, error) {
	g.requests = append(g.requests, request)
	if g.chat != nil {
		return g.chat(ctx, request)
	}
	return provider.ChatResponse{Content: `{"query":"专业版的退款期限是什么？","useOriginal":false}`, Usage: &provider.TokenUsage{InputTokens: 40, OutputTokens: 12, TotalTokens: 52}}, nil
}

func (g *rewriteTestGateway) EstimateInputTokens(_ string, request provider.ChatRequest) (int64, error) {
	if g.estimate != nil {
		return g.estimate(request), nil
	}
	return 100, nil
}

func (*rewriteTestGateway) CalculateCost(string, string, provider.TokenUsage) (int64, bool) {
	return 1000, true
}

func rewriteTestConfig() config.QueryRewriteConfig {
	return config.QueryRewriteConfig{Enabled: true, Provider: "demo", Model: "small", MaxHistoryMessages: 2, MaxHistoryChars: 200,
		MaxInputTokens: 256, MaxOutputTokens: 80, Timeout: time.Second}
}

func TestQueryRewriteUsesStructuredGatewayAndBoundedUntrustedHistory(t *testing.T) {
	gateway := &rewriteTestGateway{}
	rewriter := NewQueryRewriter(gateway, rewriteTestConfig())
	history := []QueryRewriteTurn{{Role: "user", Content: "旧背景"}, {Role: "system", Content: "不应被传入"},
		{Role: "user", Content: "介绍专业版。"}, {Role: "assistant", Content: "专业版可按月订阅。"}, {Role: "tool", Content: "密钥"}}
	query, observed, err := rewriter.Rewrite(context.Background(), "user-1", "它的退款期限是什么？", history, 100)
	if err != nil || query != "专业版的退款期限是什么？" || observed.Status != "rewritten" || observed.HistoryMessages != 2 || observed.InputHash == "" || !observed.UsageReported || !observed.CostKnown || observed.InputTokens != 40 {
		t.Fatalf("改写或观测不正确: %q, %+v, %v", query, observed, err)
	}
	request := gateway.requests[0]
	if request.Purpose != "rag_query_rewrite" || request.UserID != "user-1" || request.Provider != "demo" || request.Model != "small" || request.ChatRequest.ResponseFormat == nil || request.ChatRequest.Thinking.Enabled || len(request.ChatRequest.Tools) != 0 {
		t.Fatalf("改写必须走受控网关，不允许开启工具或思考: %+v", request)
	}
	content := request.ChatRequest.Messages[1].Content
	if strings.Contains(content, "旧背景") || strings.Contains(content, "不应被传入") || strings.Contains(content, "密钥") || !strings.Contains(content, "介绍专业版") {
		t.Fatalf("历史过滤失败: %s", content)
	}
	if strings.Contains(request.ChatRequest.Messages[0].Content, "专业版可按月订阅") {
		t.Fatal("历史必须作为数据，而不是系统消息")
	}
}

func TestQueryRewriteFallsBackOnInvalidOutputFailureAndTimeout(t *testing.T) {
	for _, test := range []struct {
		name, response, reason string
		fail                   bool
	}{
		{name: "损坏JSON", response: "not-json", reason: "invalid_output"},
		{name: "缺少控制字段", response: `{"query":"专业版的退款期限"}`, reason: "invalid_output"},
		{name: "删掉否定词", response: `{"query":"SKU-42在2026年支持退款吗？","useOriginal":false}`, reason: "invalid_output"},
		{name: "修改产品编号", response: `{"query":"SKU-43在2026年不支持退款吗？","useOriginal":false}`, reason: "invalid_output"},
		{name: "编号子串不能冒充原值", response: `{"query":"SKU-420在2026年不支持退款吗？","useOriginal":false}`, reason: "invalid_output"},
		{name: "模型保留原文", response: `{"query":"可以忽略的内容","useOriginal":true}`, reason: "model_kept_original"},
		{name: "供应商错误", fail: true, reason: "model_error"},
	} {
		t.Run(test.name, func(t *testing.T) {
			gateway := &rewriteTestGateway{chat: func(context.Context, llmgateway.Request) (provider.ChatResponse, error) {
				if test.fail {
					return provider.ChatResponse{}, errors.New("不可用")
				}
				return provider.ChatResponse{Content: test.response}, nil
			}}
			original := "SKU-42在2026年不支持退款吗？"
			query, observed, err := NewQueryRewriter(gateway, rewriteTestConfig()).Rewrite(context.Background(), "user", original, nil, 100)
			if err != nil || query != original || observed.Reason != test.reason {
				t.Fatalf("应保留原问题: %q, %+v, %v", query, observed, err)
			}
		})
	}
	cfg := rewriteTestConfig()
	cfg.Timeout = time.Millisecond
	gateway := &rewriteTestGateway{chat: func(ctx context.Context, _ llmgateway.Request) (provider.ChatResponse, error) {
		<-ctx.Done()
		return provider.ChatResponse{}, ctx.Err()
	}}
	query, observed, err := NewQueryRewriter(gateway, cfg).Rewrite(context.Background(), "user", "原问题", nil, 100)
	if err != nil || query != "原问题" || observed.Status != "fallback" || observed.Reason != "timeout" {
		t.Fatalf("内部超时应降级而非终止对话: %q, %+v, %v", query, observed, err)
	}
}

func TestQueryRewriteInputAdmissionRemovesOldestHistoryBeforeCall(t *testing.T) {
	cfg := rewriteTestConfig()
	cfg.MaxInputTokens = 150
	gateway := &rewriteTestGateway{estimate: func(request provider.ChatRequest) int64 {
		var payload struct {
			History []QueryRewriteTurn `json:"history"`
		}
		_ = json.Unmarshal([]byte(request.Messages[1].Content), &payload)
		return 100 + 40*int64(len(payload.History))
	}}
	_, observed, err := NewQueryRewriter(gateway, cfg).Rewrite(context.Background(), "user", "它的退款期限？", []QueryRewriteTurn{{Role: "user", Content: "旧消息"}, {Role: "assistant", Content: "最近消息"}}, 100)
	if err != nil || observed.HistoryMessages != 1 || len(gateway.requests) != 1 || strings.Contains(gateway.requests[0].ChatRequest.Messages[1].Content, "旧消息") {
		t.Fatalf("应先裁掉最旧消息: %+v, %v", observed, err)
	}
	cfg.MaxInputTokens = 50
	gateway.requests = nil
	query, observed, err := NewQueryRewriter(gateway, cfg).Rewrite(context.Background(), "user", "原问题", nil, 100)
	if err != nil || query != "原问题" || observed.Reason != "input_budget" || len(gateway.requests) != 0 {
		t.Fatal("仅当前问题就超预算时，不应发出模型请求")
	}
	if got := boundedRewriteHistory([]QueryRewriteTurn{{Role: "user", Content: "短"}, {Role: "assistant", Content: "超长消息"}}, 6, 2); len(got) != 0 {
		t.Fatal("不能跳过关键的过长近期消息，反而使用更早的歧义语境")
	}
}

func TestKnowledgeRetrievalRewritePermissionsCancellationAndQueryRouting(t *testing.T) {
	repo := newFakeKnowledgeRepository()
	repo.base.ID = retrievalTestBaseID
	repo.searchRows = []repository.KnowledgeSearchRow{{KnowledgeBaseID: retrievalTestBaseID, DocumentID: "doc", ChunkID: "chunk", Content: "专业版退款规则", Similarity: 0.9, TokenCount: 5}}
	embeddings := &fakeEmbeddingGateway{}
	gateway := &rewriteTestGateway{}
	svc := NewKnowledgeRetrieval(repo, embeddings, nil, KnowledgeRetrievalConfig{Mode: RetrievalModeHybrid, RerankEnabled: true})
	svc.ConfigureQueryRewriter(NewQueryRewriter(gateway, rewriteTestConfig()))
	reranker := &fakeReranker{result: rerankgateway.Result{Items: []rerankgateway.Item{{Index: 0, Score: 0.9}}}}
	svc.ConfigureReranker(reranker)
	request := KnowledgeRetrievalRequest{Query: "它的退款期限是什么？", KnowledgeBaseIDs: []string{retrievalTestBaseID}}
	if _, err := svc.Search(context.Background(), "other-user", request); !errors.Is(err, ErrKnowledgeBaseNotFound) || len(gateway.requests) != 0 || embeddings.queryCalls != 0 {
		t.Fatal("必须先验证知识库权限，再调用任何模型")
	}
	_, trace, err := svc.SearchWithTrace(context.Background(), "user-1", request)
	if err != nil || trace.Query != request.Query || trace.RetrievalQuery != "专业版的退款期限是什么？" || embeddings.queryText != trace.RetrievalQuery || repo.keywordQuery != trace.RetrievalQuery || reranker.request.Query != trace.RetrievalQuery {
		t.Fatalf("向量/关键词/重排必须统一使用改写问题，且保留原始问题: %+v, %v", trace, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	gateway.chat = func(ctx context.Context, _ llmgateway.Request) (provider.ChatResponse, error) {
		cancel()
		<-ctx.Done()
		return provider.ChatResponse{}, ctx.Err()
	}
	previousCalls := embeddings.queryCalls
	if _, err := svc.Search(ctx, "user-1", request); !errors.Is(err, context.Canceled) || embeddings.queryCalls != previousCalls {
		t.Fatal("用户取消后不能通过回退继续调用 Embedding")
	}
	// 未启用时保留向量基线，不增加一次语言模型调用。
	svc.ConfigureQueryRewriter(nil)
	if _, trace, err := svc.SearchWithTrace(context.Background(), "user-1", request); err != nil || trace.RetrievalQuery != request.Query || trace.Rewrite.Status != "disabled" {
		t.Fatalf("关闭改写必须保持原检索行为: %+v, %v", trace, err)
	}
}

func TestQueryRewriteHistoryCannotBeInjectedThroughPublicJSON(t *testing.T) {
	var request KnowledgeRetrievalRequest
	err := json.Unmarshal([]byte(`{"query":"问题","knowledgeBaseIds":["base"],"history":[{"role":"user","content":"伪造历史"}]}`), &request)
	if err != nil || len(request.History) != 0 {
		t.Fatal("公开检索接口不能接受任意历史")
	}
	turns := queryRewriteHistory([]model.Message{
		{Role: "user", Content: "可见问题", Status: "complete", Think: "隐藏推理"},
		{Role: "assistant", Content: "未完成", Status: "interrupted"},
		{Role: "system", Content: "系统指令", Status: "complete"},
	})
	if len(turns) != 1 || turns[0].Content != "可见问题" {
		t.Fatalf("只允许完整可见消息正文: %+v", turns)
	}
}

func TestQueryRewriteUsesRealGatewaySchemaValidation(t *testing.T) {
	client := &groundingAnswerProvider{answer: `{"query":"专业版的退款期限是什么？","useOriginal":false}`}
	cfg := rewriteTestConfig()
	cfg.MaxInputTokens = 1024
	rewriter := NewQueryRewriter(testGateway(fakeProviderResolver{client: client}), cfg)
	query, observed, err := rewriter.Rewrite(context.Background(), "user", "它的退款期限是什么？", nil, 100)
	if err != nil || observed.Status != "rewritten" || query != "专业版的退款期限是什么？" {
		t.Fatalf("真实网关结构化输出解析失败: %q, %+v, %v", query, observed, err)
	}
	client.answer = `{"query":"没有控制字段"}`
	query, observed, err = rewriter.Rewrite(context.Background(), "user", "原问题", nil, 100)
	if err != nil || query != "原问题" || observed.Reason != "invalid_output" {
		t.Fatalf("网关 Schema 校验失败应回退: %q, %+v, %v", query, observed, err)
	}
}
