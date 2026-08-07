package llmgateway

import (
	"context"
	"encoding/json"
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

func TestGatewayTokenBudgetUsesStrictestConfiguredLimits(t *testing.T) {
	gateway := New(
		fakeCatalog{client: &fakeClient{}, limits: provider.ModelLimits{ContextWindow: 8_000, MaxOutputTokens: 2_000}},
		HeuristicTokenEstimator{}, nil, NewMemoryIdempotencyStore(time.Hour), noopObservation{},
		Config{TokenBudget: 10_000, MaxOutputTokens: 4_000},
	)

	budget, err := gateway.TokenBudget("demo", "demo", 1_000)
	if err != nil {
		t.Fatal(err)
	}
	if budget.ContextWindow != 8_000 || budget.MaxOutputTokens != 1_000 || budget.InputTokenLimit != 7_000 {
		t.Fatalf("budget = %#v", budget)
	}
}

func TestHeuristicTokenEstimatorCountsCJKAndToolSchema(t *testing.T) {
	estimator := HeuristicTokenEstimator{}
	withoutTool := estimator.EstimateInput("demo", provider.ChatRequest{Messages: []provider.Message{{Role: "user", Content: "这是中文"}}})
	withTool := estimator.EstimateInput("demo", provider.ChatRequest{
		Messages: []provider.Message{{Role: "user", Content: "这是中文"}},
		Tools: []provider.ToolDefinition{{Function: provider.ToolFunctionDefinition{
			Name: "lookup", Description: "查询天气",
			Parameters: json.RawMessage(`{"type":"object","properties":{"城市":{"type":"string"}}}`),
		}}},
	})
	if withoutTool < 11 {
		t.Fatalf("CJK estimate = %d, want conservative framing and rune count", withoutTool)
	}
	if withTool <= withoutTool {
		t.Fatalf("tool schema was not included: without=%d with=%d", withoutTool, withTool)
	}
}

func TestModelTokenEstimatorUsesConfiguredCompatibleTokenizer(t *testing.T) {
	request := provider.ChatRequest{Model: "gpt-4o-mini", Messages: []provider.Message{{Role: "user", Content: "hello tokenizer"}}}
	estimator := NewModelTokenEstimator(fakeCatalog{tokenizerName: "o200k_base"})
	codec, err := estimator.codec("o200k_base")
	if err != nil {
		t.Fatal(err)
	}
	contentTokens, err := codec.Count("hello tokenizer")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := estimator.EstimateInput("openai", request), int64(3+4+contentTokens); got != want {
		t.Fatalf("estimate=%d want=%d", got, want)
	}

	fallback := NewModelTokenEstimator(fakeCatalog{tokenizerName: "heuristic"})
	if got, want := fallback.EstimateInput("demo", request), (HeuristicTokenEstimator{}).EstimateInput("demo", request); got != want {
		t.Fatalf("heuristic fallback=%d want=%d", got, want)
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

	purposeObservation := &recordingObservation{}
	purposeGateway := New(fakeCatalog{client: &fakeClient{}}, HeuristicTokenEstimator{}, nil, NewMemoryIdempotencyStore(time.Hour), purposeObservation, Config{TokenBudget: 1_000, MaxOutputTokens: 4})
	purposeRequest := testRequest("hello")
	purposeRequest.Purpose = "conversation_title"
	if _, err := purposeGateway.Chat(context.Background(), purposeRequest); err != nil {
		t.Fatal(err)
	}
	if got := purposeObservation.last(t).Purpose; got != "conversation_title" {
		t.Fatalf("purpose = %q", got)
	}
}

func TestGatewayValidatesStructuredChatAndRetriesInvalidResult(t *testing.T) {
	client := &fakeClient{chatResponses: []provider.ChatResponse{
		{Content: `{"answer":123}`},
		{Content: `{"answer":"可以"}`},
	}}
	gateway := newTestGateway(client, nil, Config{TokenBudget: 10_000, MaxOutputTokens: 4, SyncMaxRetries: 1, RetryBackoff: time.Millisecond})
	request := testRequest("请回答")
	request.ChatRequest.ResponseFormat = testResponseFormat()

	response, err := gateway.Chat(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if client.chatCalls != 2 || string(response.StructuredOutput) != `{"answer":"可以"}` {
		t.Fatalf("calls=%d structured=%s", client.chatCalls, response.StructuredOutput)
	}
	if len(client.requests) == 0 || client.requests[0].Messages[0].Role != "system" {
		t.Fatalf("structured instruction was not prepended: %#v", client.requests)
	}
}

func TestGatewayRejectsInvalidResponseSchemaBeforeSupplierCall(t *testing.T) {
	client := &fakeClient{}
	gateway := newTestGateway(client, nil, Config{TokenBudget: 10_000, MaxOutputTokens: 4})
	request := testRequest("hello")
	request.ChatRequest.ResponseFormat = &provider.ResponseFormat{Type: "json_schema", Name: "bad name", Schema: json.RawMessage(`{"type":"object"}`)}

	_, err := gateway.Chat(context.Background(), request)
	if !errors.Is(err, ErrInvalidResponseFormat) || client.chatCalls != 0 {
		t.Fatalf("err=%v calls=%d", err, client.chatCalls)
	}
}

func TestGatewayValidatesStructuredStreamAtDone(t *testing.T) {
	client := &fakeClient{streamEvents: []provider.StreamEvent{
		{Delta: `{"answer":`}, {Delta: `"可以"}`}, {Done: true, FinishReason: "stop"},
	}}
	gateway := newTestGateway(client, nil, Config{TokenBudget: 10_000, MaxOutputTokens: 4})
	request := testRequest("请回答")
	request.ChatRequest.ResponseFormat = testResponseFormat()

	var done provider.StreamEvent
	for event := range gateway.ChatStream(context.Background(), request) {
		if event.Err != nil {
			t.Fatal(event.Err)
		}
		if event.Done {
			done = event
		}
	}
	if !done.Done || string(done.StructuredOutput) != `{"answer":"可以"}` {
		t.Fatalf("done=%#v", done)
	}
}

func TestGatewayRejectsInvalidStructuredStreamInsteadOfDone(t *testing.T) {
	client := &fakeClient{streamEvents: []provider.StreamEvent{{Delta: `{"answer":123}`}, {Done: true}}}
	gateway := newTestGateway(client, nil, Config{TokenBudget: 10_000, MaxOutputTokens: 4})
	request := testRequest("请回答")
	request.ChatRequest.ResponseFormat = testResponseFormat()

	var streamErr error
	done := false
	for event := range gateway.ChatStream(context.Background(), request) {
		if event.Err != nil {
			streamErr = event.Err
		}
		done = done || event.Done
	}
	if !errors.Is(streamErr, ErrStructuredOutputInvalid) || done {
		t.Fatalf("err=%v done=%v", streamErr, done)
	}
}

func TestGatewayStructuredStreamDoesNotValidateToolCallTurn(t *testing.T) {
	client := &fakeClient{streamEvents: []provider.StreamEvent{
		{ToolCalls: []provider.ToolCall{{ID: "call_1", Type: "function"}}}, {Done: true},
	}}
	gateway := newTestGateway(client, nil, Config{TokenBudget: 10_000, MaxOutputTokens: 4})
	request := testRequest("调用工具")
	request.ChatRequest.ResponseFormat = testResponseFormat()

	for event := range gateway.ChatStream(context.Background(), request) {
		if event.Err != nil {
			t.Fatal(event.Err)
		}
	}
}

func testResponseFormat() *provider.ResponseFormat {
	return &provider.ResponseFormat{
		Type: "json_schema", Name: "answer",
		Schema: json.RawMessage(`{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}`),
		Strict: true,
	}
}

func newTestGateway(client provider.Provider, limiter RateLimiter, cfg Config) Gateway {
	return New(fakeCatalog{client: client}, HeuristicTokenEstimator{}, limiter, NewMemoryIdempotencyStore(time.Hour), noopObservation{}, cfg)
}

func testRequest(content string) Request {
	return Request{UserID: "user-1", Provider: "demo", Model: "demo", ChatRequest: provider.ChatRequest{Model: "demo", Messages: []provider.Message{{Role: "user", Content: content}}}}
}

type fakeCatalog struct {
	client        provider.Provider
	limits        provider.ModelLimits
	tokenizerName string
}

func (f fakeCatalog) Get(string, string) (provider.Provider, error)                 { return f.client, nil }
func (fakeCatalog) SupportsTools(string) bool                                       { return false }
func (fakeCatalog) SupportsThinking(string, string) bool                            { return false }
func (fakeCatalog) CalculateCost(string, string, provider.TokenUsage) (int64, bool) { return 0, false }
func (f fakeCatalog) Limits(string, string) (provider.ModelLimits, bool) {
	return f.limits, f.limits.ContextWindow > 0
}
func (f fakeCatalog) Tokenizer(string, string) string {
	if f.tokenizerName != "" {
		return f.tokenizerName
	}
	return "heuristic"
}

type fakeClient struct {
	chatCalls     int
	streamCalls   int
	chatErrors    []error
	chatResponses []provider.ChatResponse
	requests      []provider.ChatRequest
	streamEvents  []provider.StreamEvent
	streamDelay   time.Duration
}

func (f *fakeClient) Chat(_ context.Context, request provider.ChatRequest) (provider.ChatResponse, error) {
	f.chatCalls++
	f.requests = append(f.requests, request)
	if len(f.chatErrors) > 0 {
		err := f.chatErrors[0]
		f.chatErrors = f.chatErrors[1:]
		return provider.ChatResponse{}, err
	}
	if len(f.chatResponses) > 0 {
		response := f.chatResponses[0]
		f.chatResponses = f.chatResponses[1:]
		return response, nil
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
