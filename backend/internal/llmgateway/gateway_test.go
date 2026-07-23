package llmgateway

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/example/agent-platform/backend/internal/provider"
)

func TestGatewayRejectsBeforeClientWhenTokenBudgetExceeded(t *testing.T) {
	client := &fakeClient{}
	gateway := newTestGateway(client, nil, Config{TokenBudget: 8, MaxOutputTokens: 4})
	_, err := gateway.Chat(context.Background(), testRequest("a very long request"))
	if !errors.Is(err, ErrTokenBudgetExceeded) {
		t.Fatalf("err = %v, want token budget error", err)
	}
	if client.chatCalls != 0 {
		t.Fatalf("supplier was called %d times", client.chatCalls)
	}
}

func TestGatewayChargesRateBucketBeforeSupplierCall(t *testing.T) {
	client := &fakeClient{}
	limiter := rejectedLimiter{}
	gateway := newTestGateway(client, limiter, Config{TokenBudget: 1_000, MaxOutputTokens: 4})
	_, err := gateway.Chat(context.Background(), testRequest("hello"))
	if !errors.Is(err, ErrRateLimitExceeded) {
		t.Fatalf("err = %v, want rate limit error", err)
	}
	if client.chatCalls != 0 {
		t.Fatalf("supplier was called %d times", client.chatCalls)
	}
}

func TestIdempotencyStoreCoversWholeBusinessMessage(t *testing.T) {
	store := NewMemoryIdempotencyStore(time.Hour)
	now := time.Now()
	if err := store.Begin("turn-1", now); err != nil {
		t.Fatal(err)
	}
	if err := store.Begin("turn-1", now); !errors.Is(err, ErrIdempotencyInProgress) {
		t.Fatalf("err = %v", err)
	}
	store.Complete("turn-1", now)
	if err := store.Begin("turn-1", now); !errors.Is(err, ErrIdempotencyCompleted) {
		t.Fatalf("err = %v", err)
	}
	store.Abandon("turn-1")
	if err := store.Begin("turn-1", now); err != nil {
		t.Fatalf("abandoned operation should be reusable: %v", err)
	}
}

func TestGatewayRetriesNonStreamingCallWithoutReacquiringIdempotency(t *testing.T) {
	client := &fakeClient{chatErrors: []error{io.ErrUnexpectedEOF}}
	gateway := newTestGateway(client, nil, Config{TokenBudget: 1_000, MaxOutputTokens: 4, SyncMaxRetries: 1, RetryBackoff: time.Millisecond})
	if err := gateway.Begin(context.Background(), "turn-1"); err != nil {
		t.Fatal(err)
	}
	response, err := gateway.Chat(context.Background(), testRequest("hello"))
	if err != nil || response.Content != "ok" {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	if client.chatCalls != 2 {
		t.Fatalf("calls = %d, want 2", client.chatCalls)
	}
	if err := gateway.Begin(context.Background(), "turn-1"); !errors.Is(err, ErrIdempotencyInProgress) {
		t.Fatalf("retry must not release/reacquire operation: %v", err)
	}
}

func TestGatewayStreamAssignsMonotonicModelSequenceWithoutReplay(t *testing.T) {
	client := &fakeClient{streamEvents: []provider.StreamEvent{
		{Connected: true}, {Activity: true}, {Delta: "A"}, {Delta: "B"}, {Done: true},
	}}
	gateway := newTestGateway(client, nil, Config{TokenBudget: 1_000, MaxOutputTokens: 4, SyncMaxRetries: 3})
	var sequences []int64
	for event := range gateway.ChatStream(context.Background(), testRequest("hello")) {
		sequences = append(sequences, event.Sequence)
	}
	if len(sequences) != 5 || client.streamCalls != 1 {
		t.Fatalf("sequences=%v calls=%d", sequences, client.streamCalls)
	}
	for index, sequence := range sequences {
		if sequence != int64(index+1) {
			t.Fatalf("sequences=%v are not monotonic", sequences)
		}
	}
}

func TestGatewayObservationRecordsTTFTRetryAndFinishReason(t *testing.T) {
	streamClient := &fakeClient{
		streamDelay:  5 * time.Millisecond,
		streamEvents: []provider.StreamEvent{{Connected: true}, {Activity: true}, {Done: true, FinishReason: "stop"}},
	}
	streamObservation := &recordingObservation{}
	streamGateway := New(fakeCatalog{client: streamClient}, HeuristicTokenEstimator{}, nil, NewMemoryIdempotencyStore(time.Hour), streamObservation, Config{TokenBudget: 1_000, MaxOutputTokens: 4})
	for range streamGateway.ChatStream(context.Background(), testRequest("hello")) {
	}
	streamRecord := streamObservation.last(t)
	if streamRecord.TTFTMS <= 0 || streamRecord.RetryCount != 0 || streamRecord.FinishReason != "stop" {
		t.Fatalf("stream observation = %#v", streamRecord)
	}

	chatClient := &fakeClient{chatErrors: []error{io.ErrUnexpectedEOF}}
	chatObservation := &recordingObservation{}
	chatGateway := New(fakeCatalog{client: chatClient}, HeuristicTokenEstimator{}, nil, NewMemoryIdempotencyStore(time.Hour), chatObservation, Config{TokenBudget: 1_000, MaxOutputTokens: 4, SyncMaxRetries: 1, RetryBackoff: time.Millisecond})
	if _, err := chatGateway.Chat(context.Background(), testRequest("hello")); err != nil {
		t.Fatal(err)
	}
	chatRecord := chatObservation.last(t)
	if chatRecord.RetryCount != 1 || chatRecord.FinishReason != "stop" {
		t.Fatalf("chat observation = %#v", chatRecord)
	}
}

func newTestGateway(client provider.Provider, limiter RateLimiter, cfg Config) Gateway {
	return New(fakeCatalog{client: client}, HeuristicTokenEstimator{}, limiter, NewMemoryIdempotencyStore(time.Hour), noopObservation{}, cfg)
}

func testRequest(content string) Request {
	return Request{UserID: "user-1", Provider: "demo", Model: "demo", ChatRequest: provider.ChatRequest{Model: "demo", Messages: []provider.Message{{Role: "user", Content: content}}}}
}

type fakeCatalog struct{ client provider.Provider }

func (f fakeCatalog) Get(string, string) (provider.Provider, error)                 { return f.client, nil }
func (fakeCatalog) SupportsTools(string) bool                                       { return false }
func (fakeCatalog) SupportsThinking(string, string) bool                            { return false }
func (fakeCatalog) CalculateCost(string, string, provider.TokenUsage) (int64, bool) { return 0, false }

type fakeClient struct {
	chatCalls    int
	streamCalls  int
	chatErrors   []error
	streamEvents []provider.StreamEvent
	streamDelay  time.Duration
}

func (f *fakeClient) Chat(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
	f.chatCalls++
	if len(f.chatErrors) > 0 {
		err := f.chatErrors[0]
		f.chatErrors = f.chatErrors[1:]
		return provider.ChatResponse{}, err
	}
	return provider.ChatResponse{Content: "ok"}, nil
}

func (f *fakeClient) ChatStream(context.Context, provider.ChatRequest) <-chan provider.StreamEvent {
	f.streamCalls++
	events := make(chan provider.StreamEvent, len(f.streamEvents))
	go func() {
		defer close(events)
		for index, event := range f.streamEvents {
			if index > 0 && f.streamDelay > 0 {
				time.Sleep(f.streamDelay)
			}
			events <- event
		}
	}()
	return events
}

type noopObservation struct{}

func (noopObservation) Record(context.Context, Record) {}

type recordingObservation struct {
	mu      sync.Mutex
	records []Record
}

func (o *recordingObservation) Record(_ context.Context, record Record) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.records = append(o.records, record)
}

func (o *recordingObservation) last(t *testing.T) Record {
	t.Helper()
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.records) == 0 {
		t.Fatal("no observation records")
	}
	return o.records[len(o.records)-1]
}

type rejectedLimiter struct{}

func (rejectedLimiter) TryAcquire(context.Context, string, int64, time.Time) (bool, error) {
	return false, nil
}
