package embeddinggateway

import (
	"context"
	"errors"
	"io"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/example/agent-platform/backend/internal/config"
	"github.com/example/agent-platform/backend/internal/provider"
)

func TestNewConfiguredDisabled(t *testing.T) {
	gateway, err := NewConfigured(config.EmbeddingConfig{}, nil)
	if err != nil || gateway != nil {
		t.Fatalf("gateway=%#v err=%v", gateway, err)
	}
}

func TestGatewayBatchesByCountAndTokensAndRestoresProviderOrder(t *testing.T) {
	client := &fakeEmbeddingClient{responses: []provider.EmbeddingResponse{
		{Data: []provider.Embedding{{Index: 1, Values: []float32{0, 1}}, {Index: 0, Values: []float32{1, 0}}}, Usage: &provider.EmbeddingUsage{InputTokens: 4}},
		{Data: []provider.Embedding{{Index: 0, Values: []float32{1, 1}}}, Usage: &provider.EmbeddingUsage{InputTokens: 2}},
	}}
	price := 0.02
	gateway := New(client, fixedEstimator(2), nil, nil, Config{
		Provider: "demo", Model: "embed-demo", Dimension: 2, SendDimensions: true,
		MaxInputs: 10, BatchSize: 3, MaxInputTokens: 4, MaxBatchTokens: 4,
		RequestTimeout: time.Second, RetryBackoff: time.Millisecond, InputUSDPerMillionTokens: &price,
	})
	result, err := gateway.EmbedDocuments(context.Background(), Request{UserID: "user-1", Texts: []string{"a", "b", "c"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(client.requests) != 2 || len(client.requests[0].Input) != 2 || len(client.requests[1].Input) != 1 {
		t.Fatalf("requests = %#v", client.requests)
	}
	if result.Vectors[0].Values[0] != 1 || result.Vectors[1].Values[1] != 1 || result.Vectors[2].Values[0] != 1 {
		t.Fatalf("vectors were not restored by provider index: %#v", result.Vectors)
	}
	if result.EstimatedTokens != 6 || result.InputTokens != 6 || result.RequestCount != 2 || result.CostNanoUSD != 120 {
		t.Fatalf("result = %#v", result)
	}
}

func TestGatewayRejectsAllInputsBeforeProviderCall(t *testing.T) {
	client := &fakeEmbeddingClient{}
	gateway := New(client, fixedEstimator(5), nil, nil, Config{
		Provider: "demo", Model: "embed-demo", Dimension: 2,
		MaxInputs: 2, BatchSize: 2, MaxInputTokens: 4, MaxBatchTokens: 8,
	})
	_, err := gateway.EmbedDocuments(context.Background(), Request{UserID: "user-1", Texts: []string{"too long"}})
	if !errors.Is(err, ErrInputTokenExceeded) || len(client.requests) != 0 {
		t.Fatalf("err=%v requests=%d", err, len(client.requests))
	}
}

func TestGatewayRequiresUserForTenantScopedCache(t *testing.T) {
	client := &fakeEmbeddingClient{}
	gateway := New(client, fixedEstimator(1), nil, nil, Config{
		Provider: "demo", Model: "embed-demo", Dimension: 2,
		MaxInputs: 2, BatchSize: 2, MaxInputTokens: 4, MaxBatchTokens: 8,
	})
	_, err := gateway.EmbedDocuments(context.Background(), Request{Texts: []string{"text"}})
	if !errors.Is(err, ErrUserIDRequired) || len(client.requests) != 0 {
		t.Fatalf("err=%v requests=%d", err, len(client.requests))
	}
}

func TestGatewayCachesPerUserAndDeduplicatesWithinCall(t *testing.T) {
	cache := newMemoryEmbeddingCache()
	client := &fakeEmbeddingClient{responses: []provider.EmbeddingResponse{
		{Data: []provider.Embedding{{Index: 0, Values: []float32{1, 0}}}, Usage: &provider.EmbeddingUsage{InputTokens: 2}},
		{Data: []provider.Embedding{{Index: 0, Values: []float32{1, 0}}}, Usage: &provider.EmbeddingUsage{InputTokens: 2}},
	}}
	gateway := New(client, fixedEstimator(2), cache, nil, Config{
		Provider: "demo", Model: "embed-demo", Dimension: 2,
		MaxInputs: 10, BatchSize: 10, MaxInputTokens: 10, MaxBatchTokens: 100,
	})
	first, err := gateway.EmbedDocuments(context.Background(), Request{UserID: "user-1", Texts: []string{"same", "same"}})
	if err != nil {
		t.Fatal(err)
	}
	if first.Deduplicated != 1 || len(client.requests) != 1 || len(client.requests[0].Input) != 1 {
		t.Fatalf("first=%#v requests=%#v", first, client.requests)
	}
	second, err := gateway.EmbedDocuments(context.Background(), Request{UserID: "user-1", Texts: []string{"same"}})
	if err != nil {
		t.Fatal(err)
	}
	if second.CacheHits != 1 || second.RequestCount != 0 || !second.CostKnown || len(client.requests) != 1 {
		t.Fatalf("second=%#v requests=%d", second, len(client.requests))
	}
	if _, err := gateway.EmbedDocuments(context.Background(), Request{UserID: "user-2", Texts: []string{"same"}}); err != nil {
		t.Fatal(err)
	}
	if len(client.requests) != 2 {
		t.Fatalf("cross-user cache should miss, requests=%d", len(client.requests))
	}
}

func TestGatewayRetriesTransientFailureWithoutRetryingInvalidResponse(t *testing.T) {
	price := 0.02
	client := &fakeEmbeddingClient{
		errors:    []error{&provider.EmbeddingAPIError{StatusCode: 503, Status: "503 Service Unavailable"}},
		responses: []provider.EmbeddingResponse{{Data: []provider.Embedding{{Index: 0, Values: []float32{1, 0}}}, Usage: &provider.EmbeddingUsage{InputTokens: 1}}},
	}
	gateway := New(client, fixedEstimator(1), nil, nil, Config{
		Provider: "demo", Model: "embed-demo", Dimension: 2,
		MaxInputs: 2, BatchSize: 2, MaxInputTokens: 10, MaxBatchTokens: 10,
		MaxRetries: 1, RetryBackoff: time.Millisecond, InputUSDPerMillionTokens: &price,
	})
	result, err := gateway.EmbedDocuments(context.Background(), Request{UserID: "user-1", Texts: []string{"a"}})
	if err != nil || result.RetryCount != 1 || result.RequestCount != 2 || result.UsageReported || result.CostKnown || result.CostNanoUSD != 20 {
		t.Fatalf("result=%#v err=%v", result, err)
	}

	invalid := &fakeEmbeddingClient{responses: []provider.EmbeddingResponse{{Data: []provider.Embedding{{Index: 0, Values: []float32{1}}}}}}
	invalidGateway := New(invalid, fixedEstimator(1), nil, nil, Config{
		Provider: "demo", Model: "embed-demo", Dimension: 2,
		MaxInputs: 2, BatchSize: 2, MaxInputTokens: 10, MaxBatchTokens: 10,
		MaxRetries: 2, RetryBackoff: time.Millisecond,
	})
	_, err = invalidGateway.EmbedDocuments(context.Background(), Request{UserID: "user-1", Texts: []string{"a"}})
	if !errors.Is(err, ErrInvalidResponse) || len(invalid.requests) != 1 {
		t.Fatalf("err=%v requests=%d", err, len(invalid.requests))
	}
}

func TestGatewayRetriesUnexpectedEOFAndRejectsQueryCardinality(t *testing.T) {
	client := &fakeEmbeddingClient{
		errors:    []error{io.ErrUnexpectedEOF},
		responses: []provider.EmbeddingResponse{{Data: []provider.Embedding{{Index: 0, Values: []float32{1, 0}}}}},
	}
	gateway := New(client, fixedEstimator(1), nil, nil, Config{
		Provider: "demo", Model: "embed-demo", Dimension: 2,
		MaxInputs: 2, BatchSize: 2, MaxInputTokens: 10, MaxBatchTokens: 10,
		MaxRetries: 1, RetryBackoff: time.Millisecond,
	})
	if _, err := gateway.EmbedQuery(context.Background(), Request{UserID: "user-1", Texts: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	if len(client.requests) != 2 {
		t.Fatalf("requests=%d", len(client.requests))
	}
	if _, err := gateway.EmbedQuery(context.Background(), Request{Texts: []string{"a", "b"}}); !errors.Is(err, ErrQueryInputCount) {
		t.Fatalf("err=%v", err)
	}
}

func TestGatewayObservationDoesNotRecordInputText(t *testing.T) {
	observation := &recordingEmbeddingObservation{}
	client := &fakeEmbeddingClient{responses: []provider.EmbeddingResponse{{Data: []provider.Embedding{{Index: 0, Values: []float32{1, 0}}}}}}
	gateway := New(client, fixedEstimator(3), nil, observation, Config{
		Provider: "demo", Model: "embed-demo", Dimension: 2,
		MaxInputs: 2, BatchSize: 2, MaxInputTokens: 10, MaxBatchTokens: 10,
	})
	if _, err := gateway.EmbedDocuments(context.Background(), Request{OperationID: "op-1", UserID: "user-1", Texts: []string{"private text"}}); err != nil {
		t.Fatal(err)
	}
	record := observation.last(t)
	if record.OperationID != "op-1" || record.InputCount != 1 || record.EstimatedTokens != 3 || record.Status != "completed" {
		t.Fatalf("record=%#v", record)
	}
}

func TestValidateResponseRejectsMalformedVectors(t *testing.T) {
	tests := []struct {
		name     string
		response provider.EmbeddingResponse
		count    int
	}{
		{name: "missing vector", response: provider.EmbeddingResponse{}, count: 1},
		{name: "duplicate index", response: provider.EmbeddingResponse{Data: []provider.Embedding{
			{Index: 0, Values: []float32{1, 0}}, {Index: 0, Values: []float32{0, 1}},
		}}, count: 2},
		{name: "out of range index", response: provider.EmbeddingResponse{Data: []provider.Embedding{{Index: 1, Values: []float32{1, 0}}}}, count: 1},
		{name: "not a number", response: provider.EmbeddingResponse{Data: []provider.Embedding{{Index: 0, Values: []float32{float32(math.NaN()), 0}}}}, count: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := validateResponse(test.response, test.count, 2); !errors.Is(err, ErrInvalidResponse) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

type fixedEstimator int64

func (e fixedEstimator) Estimate(string) int64 { return int64(e) }

type fakeEmbeddingClient struct {
	mu        sync.Mutex
	requests  []provider.EmbeddingRequest
	errors    []error
	responses []provider.EmbeddingResponse
}

func (f *fakeEmbeddingClient) Embed(_ context.Context, request provider.EmbeddingRequest) (provider.EmbeddingResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, request)
	call := len(f.requests) - 1
	if call < len(f.errors) && f.errors[call] != nil {
		return provider.EmbeddingResponse{}, f.errors[call]
	}
	responseIndex := call - len(f.errors)
	if responseIndex >= 0 && responseIndex < len(f.responses) {
		return f.responses[responseIndex], nil
	}
	return provider.EmbeddingResponse{}, errors.New("unexpected embedding call")
}

type memoryEmbeddingCache struct {
	mu     sync.Mutex
	values map[string]CachedEmbedding
}

func newMemoryEmbeddingCache() *memoryEmbeddingCache {
	return &memoryEmbeddingCache{values: make(map[string]CachedEmbedding)}
}

func (c *memoryEmbeddingCache) Get(_ context.Context, key string) (CachedEmbedding, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	value, ok := c.values[key]
	return value, ok, nil
}

func (c *memoryEmbeddingCache) Set(_ context.Context, key string, value CachedEmbedding) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.values[key] = value
	return nil
}

type recordingEmbeddingObservation struct {
	mu      sync.Mutex
	records []Record
}

func (o *recordingEmbeddingObservation) Record(_ context.Context, record Record) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.records = append(o.records, record)
}

func (o *recordingEmbeddingObservation) last(t *testing.T) Record {
	t.Helper()
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.records) == 0 {
		t.Fatal("no observation records")
	}
	return o.records[len(o.records)-1]
}
