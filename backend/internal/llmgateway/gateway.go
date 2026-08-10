// Package llmgateway 是业务服务与模型 Provider 之间的唯一边界。
// 它刻意将 Provider 协议适配器置于网关之下，确保服务不会直接调用供应商 SDK 或客户端。
package llmgateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/example/agent-platform/backend/internal/logger"
	"github.com/example/agent-platform/backend/internal/provider"
	"github.com/redis/go-redis/v9"
	"github.com/tiktoken-go/tokenizer"
)

var (
	ErrTokenBudgetExceeded     = errors.New("本次模型请求预计超过 Token 预算")
	ErrRateLimitExceeded       = errors.New("模型调用额度不足，请稍后重试")
	ErrIdempotencyInProgress   = errors.New("相同幂等请求正在处理")
	ErrIdempotencyCompleted    = errors.New("相同幂等请求已经完成")
	ErrInvalidResponseFormat   = errors.New("结构化输出配置无效")
	ErrStructuredOutputInvalid = errors.New("模型返回的结构化输出不符合 Schema")
)

// LLMClient 是完成协议标准化的供应商客户端。Provider 适配器实现该接口，
// 且该接口不会暴露给业务服务。
type LLMClient interface {
	Chat(ctx context.Context, request provider.ChatRequest) (provider.ChatResponse, error)
	ChatStream(ctx context.Context, request provider.ChatRequest) <-chan provider.StreamEvent
}

// Catalog 解析已配置的客户端及其静态能力。
type Catalog interface {
	Get(id, model string) (provider.Provider, error)
	SupportsTools(id string) bool
	SupportsThinking(id, model string) bool
	CalculateCost(providerID, model string, usage provider.TokenUsage) (int64, bool)
	Limits(providerID, model string) (provider.ModelLimits, bool)
	Tokenizer(providerID, model string) string
}

// TokenEstimator 在调用供应商前估算预算；调用完成后的实际供应商用量才是计费依据。
type TokenEstimator interface {
	EstimateInput(providerID string, request provider.ChatRequest) int64
	Estimate(providerID string, request provider.ChatRequest, outputReserve int64) int64
}

// HeuristicTokenEstimator 有意采用保守且语言无关的准入估算：CJK 文本按每个
// Unicode 字符一个 Token，大多数拉丁文本约每四个 Unicode 字符一个 Token。
type HeuristicTokenEstimator struct{}

func (HeuristicTokenEstimator) EstimateInput(_ string, request provider.ChatRequest) int64 {
	var tokens int64 = 3 // request framing
	for _, message := range request.Messages {
		tokens += 4 // role and message framing
		tokens += estimateTextTokens(message.Content)
		tokens += estimateTextTokens(message.ReasoningContent)
		tokens += estimateTextTokens(message.ToolCallID)
		for _, part := range message.Parts {
			tokens += estimateTextTokens(part.Text)
			if part.Type == "image" {
				tokens += 1200
			}
		}
		for _, call := range message.ToolCalls {
			tokens += estimateTextTokens(call.ID) + estimateTextTokens(call.Function.Name) + estimateTextTokens(call.Function.Arguments)
		}
	}
	for _, definition := range request.Tools {
		tokens += 8
		tokens += estimateTextTokens(definition.Function.Name)
		tokens += estimateTextTokens(definition.Function.Description)
		tokens += estimateTextTokens(string(definition.Function.Parameters))
	}
	return tokens
}

func (e HeuristicTokenEstimator) Estimate(providerID string, request provider.ChatRequest, outputReserve int64) int64 {
	if outputReserve < 0 {
		outputReserve = 0
	}
	return e.EstimateInput(providerID, request) + outputReserve
}

// ModelTokenEstimator 为每个 Provider/模型选择已配置的官方或兼容 tokenizer，
// 加载失败时回退到保守启发式估算，避免计数故障阻断正常对话。
type ModelTokenEstimator struct {
	catalog  Catalog
	fallback HeuristicTokenEstimator
	mu       sync.RWMutex
	codecs   map[string]tokenizer.Codec
}

func NewModelTokenEstimator(catalog Catalog) *ModelTokenEstimator {
	return &ModelTokenEstimator{catalog: catalog, codecs: make(map[string]tokenizer.Codec)}
}

func (e *ModelTokenEstimator) EstimateInput(providerID string, request provider.ChatRequest) int64 {
	if e == nil {
		return (HeuristicTokenEstimator{}).EstimateInput(providerID, request)
	}
	if e.catalog == nil {
		return e.fallback.EstimateInput(providerID, request)
	}
	tokenizerName := e.catalog.Tokenizer(providerID, request.Model)
	if tokenizerName == "" || tokenizerName == "heuristic" {
		return e.fallback.EstimateInput(providerID, request)
	}
	if tokenizerName == deepSeekV4TokenizerName {
		if tokens, err := estimateDeepSeekV4Input(request); err == nil {
			return tokens + estimateContentParts(request.Messages)
		}
		return e.fallback.EstimateInput(providerID, request)
	}
	codec, err := e.codec(tokenizerName)
	if err != nil {
		return e.fallback.EstimateInput(providerID, request)
	}
	count := func(value string) int64 {
		tokens, countErr := codec.Count(value)
		if countErr != nil {
			return estimateTextTokens(value)
		}
		return int64(tokens)
	}
	var tokens int64 = 3
	for _, message := range request.Messages {
		tokens += 4 + count(message.Content) + count(message.ReasoningContent) + count(message.ToolCallID)
		for _, part := range message.Parts {
			tokens += count(part.Text)
			if part.Type == "image" {
				tokens += 1200
			}
		}
		for _, call := range message.ToolCalls {
			tokens += count(call.ID) + count(call.Function.Name) + count(call.Function.Arguments)
		}
	}
	for _, definition := range request.Tools {
		tokens += 8 + count(definition.Function.Name) + count(definition.Function.Description) + count(string(definition.Function.Parameters))
	}
	return tokens
}

func estimateContentParts(messages []provider.Message) int64 {
	var tokens int64
	for _, message := range messages {
		for _, part := range message.Parts {
			tokens += estimateTextTokens(part.Text)
			if part.Type == "image" {
				tokens += 1200
			}
		}
	}
	return tokens
}

func (e *ModelTokenEstimator) Estimate(providerID string, request provider.ChatRequest, outputReserve int64) int64 {
	if outputReserve < 0 {
		outputReserve = 0
	}
	return e.EstimateInput(providerID, request) + outputReserve
}

func (e *ModelTokenEstimator) codec(name string) (tokenizer.Codec, error) {
	e.mu.RLock()
	codec := e.codecs[name]
	e.mu.RUnlock()
	if codec != nil {
		return codec, nil
	}
	loaded, err := tokenizer.Get(tokenizer.Encoding(name))
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	if existing := e.codecs[name]; existing != nil {
		loaded = existing
	} else {
		e.codecs[name] = loaded
	}
	e.mu.Unlock()
	return loaded, nil
}

func estimateTextTokens(value string) int64 {
	var cjk, other int64
	for _, r := range value {
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r) {
			cjk++
		} else {
			other++
		}
	}
	// CJK is conservatively treated as one token per rune. Latin text usually
	// averages around four characters per token; round up to avoid undercounting.
	return cjk + (other+3)/4
}

// RateLimiter 在调用供应商前扣减预估 Token 用量。
// 实现不得等待：调用方要么立即获得容量，要么在发起任何计费请求前收到确定的准入错误。
type RateLimiter interface {
	TryAcquire(ctx context.Context, key string, tokens int64, now time.Time) (bool, error)
}

// TokenBucketLimiter stores buckets in Redis. The Lua script refills, checks
// and deducts in one atomic operation, so concurrent instances cannot each
// observe the same remaining balance and over-admit requests.
type TokenBucketLimiter struct {
	client        redis.UniversalClient
	ratePerMinute int64
	burst         int64
	keyPrefix     string
	script        *redis.Script
}

func NewTokenBucketLimiter(client redis.UniversalClient, ratePerMinute, burst int64, keyPrefix string) *TokenBucketLimiter {
	if burst <= 0 {
		burst = ratePerMinute
	}
	if keyPrefix == "" {
		keyPrefix = "agent-platform:llm:rate:"
	}
	return &TokenBucketLimiter{
		client: client, ratePerMinute: ratePerMinute, burst: burst, keyPrefix: keyPrefix,
		script: redis.NewScript(redisTokenBucketLua),
	}
}

func (l *TokenBucketLimiter) TryAcquire(ctx context.Context, key string, tokens int64, now time.Time) (bool, error) {
	if l == nil || l.ratePerMinute <= 0 || tokens <= 0 {
		return true, nil
	}
	if l.client == nil {
		return false, errors.New("redis rate limiter client is not configured")
	}
	if tokens > l.burst {
		return false, nil
	}
	// Keep idle bucket keys long enough for a full refill cycle, then let Redis
	// reclaim them. The minimum avoids hot-key churn for high rate limits.
	ttlMilliseconds := maxInt64(60_000, (l.burst*60_000/l.ratePerMinute)*2)
	result, err := l.script.Run(ctx, l.client, []string{l.keyPrefix + key}, now.UnixMilli(), l.ratePerMinute, l.burst, tokens, ttlMilliseconds).Int()
	if err != nil {
		return false, fmt.Errorf("run redis token bucket: %w", err)
	}
	return result == 1, nil
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}

const redisTokenBucketLua = `
local tokens = tonumber(redis.call('HGET', KEYS[1], 'tokens'))
local updated = tonumber(redis.call('HGET', KEYS[1], 'updated'))
local now = tonumber(ARGV[1])
local rate = tonumber(ARGV[2])
local burst = tonumber(ARGV[3])
local requested = tonumber(ARGV[4])
local ttl = tonumber(ARGV[5])

if tokens == nil then tokens = burst end
if updated == nil then updated = now end
local elapsed = math.max(0, now - updated)
tokens = math.min(burst, tokens + elapsed * rate / 60000)

redis.call('HSET', KEYS[1], 'tokens', tokens, 'updated', now)
redis.call('PEXPIRE', KEYS[1], ttl)
if tokens < requested then return 0 end
redis.call('HSET', KEYS[1], 'tokens', tokens - requested)
return 1
`

// IdempotencyStore 锁定完整业务消息，而非单次供应商调用。
// 因此 Gateway 内部的重试不会再次获取该锁。
type IdempotencyStore interface {
	Begin(key string, now time.Time) error
	Complete(key string, now time.Time)
	Abandon(key string)
}

type memoryIdempotencyStore struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]idempotencyEntry
}

type idempotencyEntry struct {
	completed bool
	expiresAt time.Time
}

func NewMemoryIdempotencyStore(ttl time.Duration) IdempotencyStore {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return &memoryIdempotencyStore{ttl: ttl, entries: make(map[string]idempotencyEntry)}
}

func (s *memoryIdempotencyStore) Begin(key string, now time.Time) error {
	if key == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry, ok := s.entries[key]; ok && now.Before(entry.expiresAt) {
		if entry.completed {
			return ErrIdempotencyCompleted
		}
		return ErrIdempotencyInProgress
	}
	s.entries[key] = idempotencyEntry{expiresAt: now.Add(s.ttl)}
	return nil
}

func (s *memoryIdempotencyStore) Complete(key string, now time.Time) {
	if key == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[key] = idempotencyEntry{completed: true, expiresAt: now.Add(s.ttl)}
}

func (s *memoryIdempotencyStore) Abandon(key string) {
	if key == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, key)
}

// Observation 接收生命周期信息，同时避免 Gateway 与特定链路追踪供应商耦合。
// 默认实现会输出结构化 Zap 日志。
type Observation interface {
	Record(ctx context.Context, record Record)
}

type Record struct {
	OperationID     string
	Purpose         string
	UserID          string
	Provider        string
	Model           string
	Mode            string
	Turn            int
	Attempt         int
	EstimatedTokens int64
	ActualTokens    int64
	Sequence        int64
	TTFTMS          int64
	RetryCount      int
	FinishReason    string
	Duration        time.Duration
	Status          string
	Err             error
}

type ZapObservation struct{}

func (ZapObservation) Record(ctx context.Context, record Record) {
	fields := []logger.Field{
		logger.String("operation_id", record.OperationID), logger.String("user_id", record.UserID),
		logger.String("purpose", record.Purpose),
		logger.String("provider", record.Provider), logger.String("model", record.Model),
		logger.String("mode", record.Mode), logger.Int("turn", record.Turn), logger.Int("attempt", record.Attempt),
		logger.Int64("estimated_tokens", record.EstimatedTokens), logger.Int64("actual_tokens", record.ActualTokens), logger.Int64("stream_sequence", record.Sequence),
		logger.Int64("ttft_ms", record.TTFTMS), logger.Int("retry_count", record.RetryCount),
		logger.Int64("duration_ms", record.Duration.Milliseconds()), logger.String("finish_reason", record.FinishReason), logger.String("status", record.Status),
	}
	if record.Err != nil {
		fields = append(fields, logger.Err(record.Err))
	}
	if record.Err != nil {
		logger.Warn(ctx, "llm gateway observation", fields...)
	} else {
		logger.Info(ctx, "llm gateway observation", fields...)
	}
}

type Config struct {
	TokenBudget     int64
	MaxOutputTokens int64
	SyncMaxRetries  int
	RetryBackoff    time.Duration
}

// Request 表示业务消息中的一次模型轮次。Turn 用于工具调用循环遥测；
// 重试次数仅在 Gateway 内部使用。
type Request struct {
	OperationID string
	Purpose     string
	UserID      string
	Provider    string
	Model       string
	Turn        int
	ChatRequest provider.ChatRequest
}

type TokenBudget struct {
	ContextWindow   int64
	MaxOutputTokens int64
	InputTokenLimit int64
}

// Gateway 是服务中唯一允许依赖的模型调用组件。
type Gateway interface {
	Validate(providerID, model string) error
	ValidateResponseFormat(format *provider.ResponseFormat) error
	SupportsTools(providerID string) bool
	SupportsThinking(providerID, model string) bool
	CalculateCost(providerID, model string, usage provider.TokenUsage) (int64, bool)
	TokenBudget(providerID, model string, requestedOutput int64) (TokenBudget, error)
	Tokenizer(providerID, model string) string
	EstimateInputTokens(providerID string, request provider.ChatRequest) (int64, error)
	Begin(ctx context.Context, operationID string) error
	Complete(operationID string)
	Abandon(operationID string)
	Chat(ctx context.Context, request Request) (provider.ChatResponse, error)
	ChatStream(ctx context.Context, request Request) <-chan provider.StreamEvent
}

type gateway struct {
	catalog     Catalog
	estimator   TokenEstimator
	limiter     RateLimiter
	idempotency IdempotencyStore
	observation Observation
	config      Config
}

func New(catalog Catalog, estimator TokenEstimator, limiter RateLimiter, idempotency IdempotencyStore, observation Observation, cfg Config) Gateway {
	if estimator == nil {
		estimator = HeuristicTokenEstimator{}
	}
	if idempotency == nil {
		idempotency = NewMemoryIdempotencyStore(24 * time.Hour)
	}
	if observation == nil {
		observation = ZapObservation{}
	}
	if cfg.MaxOutputTokens <= 0 {
		cfg.MaxOutputTokens = 4096
	}
	if cfg.TokenBudget <= 0 {
		cfg.TokenBudget = 32_768
	}
	if cfg.SyncMaxRetries < 0 {
		cfg.SyncMaxRetries = 0
	}
	if cfg.RetryBackoff <= 0 {
		cfg.RetryBackoff = 200 * time.Millisecond
	}
	return &gateway{catalog: catalog, estimator: estimator, limiter: limiter, idempotency: idempotency, observation: observation, config: cfg}
}

func (g *gateway) Validate(providerID, model string) error {
	_, err := g.catalog.Get(providerID, model)
	return err
}
func (g *gateway) ValidateResponseFormat(format *provider.ResponseFormat) error {
	_, err := compileResponseFormat(format)
	return err
}
func (g *gateway) SupportsTools(providerID string) bool { return g.catalog.SupportsTools(providerID) }
func (g *gateway) SupportsThinking(providerID, model string) bool {
	return g.catalog.SupportsThinking(providerID, model)
}
func (g *gateway) CalculateCost(providerID, model string, usage provider.TokenUsage) (int64, bool) {
	return g.catalog.CalculateCost(providerID, model, usage)
}
func (g *gateway) Tokenizer(providerID, model string) string {
	return g.catalog.Tokenizer(providerID, model)
}
func (g *gateway) TokenBudget(providerID, model string, requestedOutput int64) (TokenBudget, error) {
	if _, err := g.catalog.Get(providerID, model); err != nil {
		return TokenBudget{}, err
	}
	contextWindow := g.config.TokenBudget
	maxOutput := g.config.MaxOutputTokens
	if limits, ok := g.catalog.Limits(providerID, model); ok {
		if limits.ContextWindow > 0 && limits.ContextWindow < contextWindow {
			contextWindow = limits.ContextWindow
		}
		if limits.MaxOutputTokens > 0 && limits.MaxOutputTokens < maxOutput {
			maxOutput = limits.MaxOutputTokens
		}
	}
	if requestedOutput > 0 && requestedOutput < maxOutput {
		maxOutput = requestedOutput
	}
	if maxOutput >= contextWindow {
		return TokenBudget{}, fmt.Errorf("model output reservation %d must be smaller than context window %d", maxOutput, contextWindow)
	}
	return TokenBudget{ContextWindow: contextWindow, MaxOutputTokens: maxOutput, InputTokenLimit: contextWindow - maxOutput}, nil
}

func (g *gateway) EstimateInputTokens(providerID string, request provider.ChatRequest) (int64, error) {
	prepared, _, err := prepareStructuredRequest(Request{ChatRequest: request})
	if err != nil {
		return 0, err
	}
	return g.estimator.EstimateInput(providerID, prepared.ChatRequest), nil
}
func (g *gateway) Begin(_ context.Context, operationID string) error {
	return g.idempotency.Begin(operationID, time.Now())
}
func (g *gateway) Complete(operationID string) { g.idempotency.Complete(operationID, time.Now()) }
func (g *gateway) Abandon(operationID string)  { g.idempotency.Abandon(operationID) }

func (g *gateway) preflight(ctx context.Context, request Request) (LLMClient, int64, error) {
	client, err := g.catalog.Get(request.Provider, request.Model)
	if err != nil {
		return nil, 0, err
	}
	budget, err := g.TokenBudget(request.Provider, request.Model, request.ChatRequest.MaxOutputTokens)
	if err != nil {
		return nil, 0, err
	}
	estimated := g.estimator.Estimate(request.Provider, request.ChatRequest, budget.MaxOutputTokens)
	if estimated > budget.ContextWindow {
		return nil, estimated, ErrTokenBudgetExceeded
	}
	key := request.UserID + ":" + request.Provider + ":" + request.Model
	if g.limiter != nil {
		granted, err := g.limiter.TryAcquire(ctx, key, estimated, time.Now())
		if err != nil {
			return nil, estimated, fmt.Errorf("acquire rate limit: %w", err)
		}
		if !granted {
			return nil, estimated, ErrRateLimitExceeded
		}
	}
	return client, estimated, nil
}

func (g *gateway) Chat(ctx context.Context, request Request) (provider.ChatResponse, error) {
	request, schema, err := prepareStructuredRequest(request)
	if err != nil {
		g.observe(ctx, request, "chat", 0, 0, 0, 0, "rejected", err, time.Now(), 0, finishReasonForError(err))
		return provider.ChatResponse{}, err
	}
	budget, err := g.TokenBudget(request.Provider, request.Model, request.ChatRequest.MaxOutputTokens)
	if err != nil {
		return provider.ChatResponse{}, err
	}
	request.ChatRequest.MaxOutputTokens = budget.MaxOutputTokens
	client, estimated, err := g.preflight(ctx, request)
	if err != nil {
		g.observe(ctx, request, "chat", 0, estimated, 0, 0, "rejected", err, time.Now(), 0, finishReasonForError(err))
		return provider.ChatResponse{}, err
	}
	var aggregateUsage provider.TokenUsage
	usageReported := false
	for attempt := 1; ; attempt++ {
		started := time.Now()
		response, callErr := client.Chat(ctx, request.ChatRequest)
		attemptActual := int64(0)
		if response.Usage != nil {
			attemptActual = response.Usage.TotalTokens
			aggregateUsage.Add(*response.Usage)
			usageReported = true
		}
		if callErr == nil && schema != nil && len(response.ToolCalls) == 0 {
			response.StructuredOutput, callErr = validateStructuredContent(schema, response.Content)
		}
		if callErr == nil {
			if usageReported {
				response.Usage = &aggregateUsage
			}
			g.observe(ctx, request, "chat", attempt, estimated, attemptActual, 0, "completed", nil, started, 0, "stop")
			return response, nil
		}
		g.observe(ctx, request, "chat", attempt, estimated, attemptActual, 0, "failed", callErr, started, 0, finishReasonForError(callErr))
		if attempt > g.config.SyncMaxRetries || (!retryable(ctx, callErr) && !errors.Is(callErr, ErrStructuredOutputInvalid)) {
			if usageReported {
				response.Usage = &aggregateUsage
			}
			return response, callErr
		}
		if !waitRetry(ctx, time.Duration(attempt)*g.config.RetryBackoff) {
			return provider.ChatResponse{}, ctx.Err()
		}
	}
}

func (g *gateway) ChatStream(ctx context.Context, request Request) <-chan provider.StreamEvent {
	output := make(chan provider.StreamEvent)
	request, schema, err := prepareStructuredRequest(request)
	if err != nil {
		go func() {
			defer close(output)
			g.observe(ctx, request, "stream", 0, 0, 0, 0, "rejected", err, time.Now(), 0, finishReasonForError(err))
			output <- provider.StreamEvent{Err: err}
		}()
		return output
	}
	budget, err := g.TokenBudget(request.Provider, request.Model, request.ChatRequest.MaxOutputTokens)
	if err != nil {
		go func() {
			defer close(output)
			output <- provider.StreamEvent{Err: err}
		}()
		return output
	}
	request.ChatRequest.MaxOutputTokens = budget.MaxOutputTokens
	client, estimated, err := g.preflight(ctx, request)
	if err != nil {
		go func() {
			defer close(output)
			g.observe(ctx, request, "stream", 0, estimated, 0, 0, "rejected", err, time.Now(), 0, finishReasonForError(err))
			output <- provider.StreamEvent{Err: err}
		}()
		return output
	}
	go func() {
		defer close(output)
		started := time.Now()
		sequence := int64(0)
		var usage provider.TokenUsage
		var connectedAt time.Time
		var ttftMS int64
		var content strings.Builder
		hasToolCalls := false
		status := "interrupted"
		var finalErr error
		// 流式请求刻意不重试；一旦流已对外暴露，重放可能造成 Token、工具副作用和供应商费用重复。
		for event := range client.ChatStream(ctx, request.ChatRequest) {
			sequence++
			event.Sequence = sequence
			if event.Connected && connectedAt.IsZero() {
				connectedAt = time.Now()
			}
			if event.Activity && ttftMS == 0 {
				base := connectedAt
				if base.IsZero() {
					base = started
				}
				ttftMS = time.Since(base).Milliseconds()
			}
			if event.Delta != "" {
				content.WriteString(event.Delta)
			}
			if len(event.ToolCalls) > 0 {
				hasToolCalls = true
			}
			if event.Done && schema != nil && !hasToolCalls {
				structured, validationErr := validateStructuredContent(schema, content.String())
				if validationErr != nil {
					event = provider.StreamEvent{Sequence: sequence, Err: validationErr}
				} else {
					event.StructuredOutput = structured
				}
			}
			select {
			case <-ctx.Done():
				finalErr = ctx.Err()
				status = "canceled"
				g.observe(ctx, request, "stream", 1, estimated, usage.TotalTokens, sequence, status, finalErr, started, ttftMS, finishReasonForError(context.Cause(ctx)))
				return
			case output <- event:
			}
			if event.Usage != nil {
				usage.Merge(*event.Usage)
			}
			if event.Err != nil {
				finalErr = event.Err
				status = "failed"
				g.observe(ctx, request, "stream", 1, estimated, usage.TotalTokens, sequence, status, finalErr, started, ttftMS, finishReasonForError(finalErr))
				return
			}
			if event.Done {
				status = "completed"
				finishReason := event.FinishReason
				if finishReason == "" {
					finishReason = "stop"
				}
				g.observe(ctx, request, "stream", 1, estimated, usage.TotalTokens, sequence, status, nil, started, ttftMS, finishReason)
				return
			}
		}
		if ctx.Err() != nil {
			finalErr = ctx.Err()
			status = "canceled"
		} else {
			finalErr = provider.NewStreamError(provider.StreamErrorDisconnected, io.ErrUnexpectedEOF)
		}
		g.observe(ctx, request, "stream", 1, estimated, usage.TotalTokens, sequence, status, finalErr, started, ttftMS, finishReasonForError(finalErr))
	}()
	return output
}

func (g *gateway) observe(ctx context.Context, request Request, mode string, attempt int, estimated, actual, sequence int64, status string, err error, started time.Time, ttftMS int64, finishReason string) {
	retryCount := attempt - 1
	if retryCount < 0 {
		retryCount = 0
	}
	g.observation.Record(ctx, Record{OperationID: request.OperationID, Purpose: request.Purpose, UserID: request.UserID, Provider: request.Provider, Model: request.Model, Mode: mode, Turn: request.Turn, Attempt: attempt, EstimatedTokens: estimated, ActualTokens: actual, Sequence: sequence, TTFTMS: ttftMS, RetryCount: retryCount, FinishReason: finishReason, Duration: time.Since(started), Status: status, Err: err})
}

func finishReasonForError(err error) string {
	if err == nil {
		return "stop"
	}
	if errors.Is(err, ErrRateLimitExceeded) {
		return "rate_limit_exceeded"
	}
	if errors.Is(err, ErrTokenBudgetExceeded) {
		return "token_budget_exceeded"
	}
	if errors.Is(err, ErrInvalidResponseFormat) {
		return "invalid_response_format"
	}
	if errors.Is(err, ErrStructuredOutputInvalid) {
		return "structured_output_invalid"
	}
	return string(provider.StreamErrorKindOf(err))
}

func retryable(ctx context.Context, err error) bool {
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) {
		return true
	}
	return false
}

func waitRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func OperationID(userID, conversationID, idempotencyKey string) string {
	if idempotencyKey == "" {
		return ""
	}
	return fmt.Sprintf("%s:%s:%s", userID, conversationID, idempotencyKey)
}
