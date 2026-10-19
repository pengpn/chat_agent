package rerankgateway

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/example/agent-platform/backend/internal/config"
	"github.com/example/agent-platform/backend/internal/provider"
)

type clientFunc func(context.Context, provider.RerankRequest) (provider.RerankResponse, error)

func (f clientFunc) Rerank(ctx context.Context, req provider.RerankRequest) (provider.RerankResponse, error) {
	return f(ctx, req)
}

type fixedEstimator int64

func (e fixedEstimator) Estimate(string) int64 { return int64(e) }

func testConfig() config.RerankConfig {
	return config.RerankConfig{Provider: "fake", Model: "rank-v1", MaxDocuments: 3, MaxPairTokens: 10, MaxTotalTokens: 20, RequestTimeout: time.Second}
}

func TestGatewayValidatesBudgetAndMapsCompleteResults(t *testing.T) {
	calls := 0
	client := clientFunc(func(_ context.Context, req provider.RerankRequest) (provider.RerankResponse, error) {
		calls++
		if req.Model != "rank-v1" || len(req.Documents) != 2 {
			t.Fatalf("错误的上游请求: %+v", req)
		}
		return provider.RerankResponse{Items: []provider.RerankItem{{Index: 1, Score: 0.9}, {Index: 0, Score: 0.2}}}, nil
	})
	gw := New(client, fixedEstimator(3), testConfig())
	result, err := gw.Rerank(context.Background(), Request{UserID: "u", Query: "q", Documents: []string{"a", "b"}})
	if err != nil || result.Items[0].Index != 1 || result.EstimatedTokens != 12 || result.InputTokens != nil || result.BilledSearchUnits != nil || result.Model != "rank-v1" {
		t.Fatalf("错误的重排结果: %+v, %v", result, err)
	}
	_, err = gw.Rerank(context.Background(), Request{UserID: "u", Query: "q", Documents: []string{"a", "b", "c", "d"}})
	if !errors.Is(err, ErrBudgetExceeded) || calls != 1 {
		t.Fatal("数量超限必须在请求前拒绝")
	}
	cfg := testConfig()
	cfg.MaxTotalTokens = 10
	_, err = New(client, fixedEstimator(3), cfg).Rerank(context.Background(), Request{UserID: "u", Query: "q", Documents: []string{"a", "b"}})
	if !errors.Is(err, ErrBudgetExceeded) || calls != 1 {
		t.Fatal("总 Token 超限必须在请求前拒绝")
	}
	cfg.MaxPairTokens = 5
	_, err = New(client, fixedEstimator(3), cfg).Rerank(context.Background(), Request{UserID: "u", Query: "q", Documents: []string{"a"}})
	if !errors.Is(err, ErrBudgetExceeded) || calls != 1 {
		t.Fatal("单对 Token 超限必须在请求前拒绝")
	}
}

func TestGatewayPropagatesCancellationAndTimeout(t *testing.T) {
	client := clientFunc(func(ctx context.Context, _ provider.RerankRequest) (provider.RerankResponse, error) {
		<-ctx.Done()
		return provider.RerankResponse{}, ctx.Err()
	})
	cfg := testConfig()
	cfg.RequestTimeout = 5 * time.Millisecond
	gw := New(client, fixedEstimator(1), cfg)
	_, err := gw.Rerank(context.Background(), Request{UserID: "u", Query: "q", Documents: []string{"a"}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("超时未传播: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = gw.Rerank(ctx, Request{UserID: "u", Query: "q", Documents: []string{"a"}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("取消未传播: %v", err)
	}
}

func TestOrderRejectsInvalidPermutationsAndKeepsStableTies(t *testing.T) {
	for _, items := range [][]Item{
		{{Index: 0, Score: 1}}, {{Index: 0, Score: 1}, {Index: 0, Score: 0}},
		{{Index: 0, Score: 1}, {Index: 2, Score: 0}}, {{Index: -1, Score: 1}, {Index: 1, Score: 0}},
		{{Index: 0, Score: math.NaN()}, {Index: 1, Score: 0}}, {{Index: 0, Score: math.Inf(1)}, {Index: 1, Score: 0}},
	} {
		if _, err := Order(items, 2); !errors.Is(err, ErrInvalidResponse) {
			t.Fatalf("无效排列未拒绝: %+v", items)
		}
	}
	ordered, err := Order([]Item{{Index: 1, Score: 0}, {Index: 0, Score: 0}}, 2)
	if err != nil || ordered[0].Index != 0 {
		t.Fatal("零分有效，同分应按原始位置排序")
	}
}
