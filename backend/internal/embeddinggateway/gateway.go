// Package embeddinggateway is the only business-facing boundary for vector
// generation. It owns token admission, batching, retries, validation, cache,
// cost accounting and observations; services never call embedding APIs directly.
package embeddinggateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"strings"
	"time"

	"github.com/example/agent-platform/backend/internal/logger"
	"github.com/example/agent-platform/backend/internal/provider"
)

var (
	ErrNotConfigured      = errors.New("EmbeddingGateway 未配置")
	ErrUserIDRequired     = errors.New("Embedding 用户标识不能为空")
	ErrNoInput            = errors.New("Embedding 输入不能为空")
	ErrTooManyInputs      = errors.New("Embedding 输入数量超过限制")
	ErrInputTokenExceeded = errors.New("单条 Embedding 输入超过 Token 限制")
	ErrInvalidResponse    = errors.New("Embedding 服务返回无效结果")
	ErrQueryInputCount    = errors.New("查询 Embedding 必须且只能包含一条文本")
)

// EmbeddingClient is implemented by provider protocol adapters. It is kept
// deliberately small so gateway tests can inject a fake without HTTP.
type EmbeddingClient interface {
	Embed(ctx context.Context, request provider.EmbeddingRequest) (provider.EmbeddingResponse, error)
}

type Config struct {
	Provider                 string
	Model                    string
	Dimension                int
	SendDimensions           bool
	MaxInputs                int
	BatchSize                int
	MaxInputTokens           int64
	MaxBatchTokens           int64
	RequestTimeout           time.Duration
	MaxRetries               int
	RetryBackoff             time.Duration
	InputUSDPerMillionTokens *float64
}

type Request struct {
	OperationID string
	Purpose     string
	UserID      string
	Texts       []string
}

type Vector struct {
	Index       int
	Values      []float32
	ContentHash string
	CacheHit    bool
}

type Result struct {
	Provider        string
	Model           string
	Dimension       int
	Vectors         []Vector
	EstimatedTokens int64
	InputTokens     int64
	UsageReported   bool
	CostNanoUSD     int64
	CostKnown       bool
	CacheHits       int
	Deduplicated    int
	RequestCount    int
	RetryCount      int
}

type Observation interface {
	Record(ctx context.Context, record Record)
}

type Record struct {
	OperationID     string
	Purpose         string
	UserID          string
	Provider        string
	Model           string
	InputCount      int
	Dimension       int
	EstimatedTokens int64
	ActualTokens    int64
	UsageReported   bool
	CacheHits       int
	Deduplicated    int
	RequestCount    int
	RetryCount      int
	CostNanoUSD     int64
	CostKnown       bool
	Duration        time.Duration
	Status          string
	Err             error
}

type ZapObservation struct{}

func (ZapObservation) Record(ctx context.Context, record Record) {
	fields := []logger.Field{
		logger.String("operation_id", record.OperationID), logger.String("purpose", record.Purpose),
		logger.String("user_id", record.UserID), logger.String("provider", record.Provider), logger.String("model", record.Model),
		logger.Int("input_count", record.InputCount), logger.Int("embedding_dimension", record.Dimension),
		logger.Int64("estimated_tokens", record.EstimatedTokens), logger.Int64("actual_tokens", record.ActualTokens),
		logger.Bool("usage_reported", record.UsageReported),
		logger.Int("cache_hits", record.CacheHits), logger.Int("deduplicated", record.Deduplicated),
		logger.Int("request_count", record.RequestCount), logger.Int("retry_count", record.RetryCount),
		logger.Int64("cost_nano_usd", record.CostNanoUSD), logger.Bool("cost_known", record.CostKnown),
		logger.Int64("duration_ms", record.Duration.Milliseconds()), logger.String("status", record.Status),
	}
	if record.Err != nil {
		fields = append(fields, logger.Err(record.Err))
		logger.Warn(ctx, "embedding gateway observation", fields...)
		return
	}
	logger.Info(ctx, "embedding gateway observation", fields...)
}

type Gateway interface {
	EmbedDocuments(ctx context.Context, request Request) (Result, error)
	EmbedQuery(ctx context.Context, request Request) (Result, error)
}

type gateway struct {
	client      EmbeddingClient
	estimator   TokenEstimator
	cache       Cache
	observation Observation
	config      Config
}

func New(client EmbeddingClient, estimator TokenEstimator, cache Cache, observation Observation, cfg Config) Gateway {
	if estimator == nil {
		estimator = HeuristicTokenEstimator{}
	}
	if observation == nil {
		observation = ZapObservation{}
	}
	if cfg.MaxInputs <= 0 {
		cfg.MaxInputs = 2048
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 64
	}
	if cfg.MaxInputTokens <= 0 {
		cfg.MaxInputTokens = 8191
	}
	if cfg.MaxBatchTokens <= 0 {
		cfg.MaxBatchTokens = 100_000
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 30 * time.Second
	}
	if cfg.MaxRetries < 0 {
		cfg.MaxRetries = 0
	}
	if cfg.RetryBackoff <= 0 {
		cfg.RetryBackoff = 300 * time.Millisecond
	}
	return &gateway{client: client, estimator: estimator, cache: cache, observation: observation, config: cfg}
}

func (g *gateway) EmbedDocuments(ctx context.Context, request Request) (Result, error) {
	if request.Purpose == "" {
		request.Purpose = "rag_document"
	}
	return g.embed(ctx, request)
}

func (g *gateway) EmbedQuery(ctx context.Context, request Request) (Result, error) {
	if request.Purpose == "" {
		request.Purpose = "rag_query"
	}
	if len(request.Texts) != 1 {
		started := time.Now()
		g.observe(ctx, request, Result{}, "rejected", ErrQueryInputCount, started)
		return Result{}, ErrQueryInputCount
	}
	return g.embed(ctx, request)
}

type pendingInput struct {
	text          string
	contentHash   string
	cacheKey      string
	tokens        int64
	targetIndices []int
}

func (g *gateway) embed(ctx context.Context, request Request) (Result, error) {
	started := time.Now()
	result := Result{Provider: g.config.Provider, Model: g.config.Model, Dimension: g.config.Dimension, Vectors: make([]Vector, len(request.Texts)), UsageReported: true}
	if g.client == nil || g.config.Provider == "" || g.config.Model == "" || g.config.Dimension <= 0 {
		g.observe(ctx, request, result, "rejected", ErrNotConfigured, started)
		return Result{}, ErrNotConfigured
	}
	if strings.TrimSpace(request.UserID) == "" {
		g.observe(ctx, request, result, "rejected", ErrUserIDRequired, started)
		return Result{}, ErrUserIDRequired
	}
	if len(request.Texts) == 0 {
		g.observe(ctx, request, result, "rejected", ErrNoInput, started)
		return Result{}, ErrNoInput
	}
	if len(request.Texts) > g.config.MaxInputs {
		g.observe(ctx, request, result, "rejected", ErrTooManyInputs, started)
		return Result{}, ErrTooManyInputs
	}

	pending := make([]pendingInput, 0, len(request.Texts))
	pendingByKey := make(map[string]int, len(request.Texts))
	for index, rawText := range request.Texts {
		if strings.TrimSpace(rawText) == "" {
			err := fmt.Errorf("%w: index %d", ErrNoInput, index)
			g.observe(ctx, request, result, "rejected", err, started)
			return Result{}, err
		}
		tokens := g.estimator.Estimate(rawText)
		if tokens <= 0 {
			tokens = 1
		}
		if tokens > g.config.MaxInputTokens {
			err := fmt.Errorf("%w: index %d estimated %d, limit %d", ErrInputTokenExceeded, index, tokens, g.config.MaxInputTokens)
			result.EstimatedTokens += tokens
			g.observe(ctx, request, result, "rejected", err, started)
			return Result{}, err
		}
		contentHash := hashString(rawText)
		key := g.cacheKey(request.UserID, contentHash)
		result.Vectors[index] = Vector{Index: index, ContentHash: contentHash}
		if existing, ok := pendingByKey[key]; ok {
			pending[existing].targetIndices = append(pending[existing].targetIndices, index)
			result.Deduplicated++
			continue
		}
		if g.cache != nil {
			cached, found, err := g.cache.Get(ctx, key)
			if err != nil {
				logger.Warn(ctx, "embedding cache read failed; falling back to provider", logger.Err(err))
			} else if found && cached.Dimension == g.config.Dimension && validVector(cached.Values, g.config.Dimension) {
				result.Vectors[index].Values = cloneVector(cached.Values)
				result.Vectors[index].CacheHit = true
				result.CacheHits++
				continue
			}
		}
		pendingByKey[key] = len(pending)
		pending = append(pending, pendingInput{text: rawText, contentHash: contentHash, cacheKey: key, tokens: tokens, targetIndices: []int{index}})
		result.EstimatedTokens += tokens
	}

	batches := g.makeBatches(pending)
	for _, batch := range batches {
		response, retries, err := g.callBatch(ctx, batch)
		result.RetryCount += retries
		result.RequestCount += retries + 1
		if retries > 0 {
			// A timed-out or disconnected attempt may have been billed even though
			// it could not return usage. The successful retry is only a lower bound.
			result.UsageReported = false
		}
		if err != nil {
			g.observe(ctx, request, result, "failed", err, started)
			return Result{}, err
		}
		ordered, err := validateResponse(response, len(batch), g.config.Dimension)
		if err != nil {
			g.observe(ctx, request, result, "failed", err, started)
			return Result{}, err
		}
		if response.Usage == nil {
			result.UsageReported = false
		} else {
			result.InputTokens += response.Usage.InputTokens
		}
		for batchIndex, item := range batch {
			values := ordered[batchIndex]
			for _, targetIndex := range item.targetIndices {
				result.Vectors[targetIndex].Values = cloneVector(values)
			}
			if g.cache != nil {
				if err := g.cache.Set(ctx, item.cacheKey, CachedEmbedding{Values: cloneVector(values), Dimension: g.config.Dimension}); err != nil {
					logger.Warn(ctx, "embedding cache write failed", logger.Err(err))
				}
			}
		}
	}
	if result.RequestCount == 0 {
		result.CostKnown = true
	} else if result.InputTokens > 0 && g.config.InputUSDPerMillionTokens != nil {
		result.CostNanoUSD = int64(math.Round(float64(result.InputTokens) * *g.config.InputUSDPerMillionTokens * 1_000))
		result.CostKnown = result.UsageReported
	}
	g.observe(ctx, request, result, "completed", nil, started)
	return result, nil
}

func (g *gateway) makeBatches(pending []pendingInput) [][]pendingInput {
	result := make([][]pendingInput, 0)
	for len(pending) > 0 {
		count := 0
		var tokens int64
		for count < len(pending) && count < g.config.BatchSize {
			if count > 0 && tokens+pending[count].tokens > g.config.MaxBatchTokens {
				break
			}
			tokens += pending[count].tokens
			count++
		}
		result = append(result, pending[:count])
		pending = pending[count:]
	}
	return result
}

func (g *gateway) callBatch(ctx context.Context, batch []pendingInput) (provider.EmbeddingResponse, int, error) {
	inputs := make([]string, len(batch))
	for index := range batch {
		inputs[index] = batch[index].text
	}
	dimensions := 0
	if g.config.SendDimensions {
		dimensions = g.config.Dimension
	}
	request := provider.EmbeddingRequest{Model: g.config.Model, Input: inputs, Dimensions: dimensions}
	for attempt := 0; ; attempt++ {
		callCtx, cancel := context.WithTimeout(ctx, g.config.RequestTimeout)
		response, err := g.client.Embed(callCtx, request)
		cancel()
		if err == nil {
			return response, attempt, nil
		}
		if attempt >= g.config.MaxRetries || !retryable(ctx, err) {
			return provider.EmbeddingResponse{}, attempt, err
		}
		delay := time.Duration(1<<attempt) * g.config.RetryBackoff
		var apiErr *provider.EmbeddingAPIError
		if errors.As(err, &apiErr) && apiErr.RetryAfter > delay {
			delay = apiErr.RetryAfter
		}
		if !waitRetry(ctx, delay) {
			return provider.EmbeddingResponse{}, attempt, context.Cause(ctx)
		}
	}
}

func validateResponse(response provider.EmbeddingResponse, expectedCount, dimension int) ([][]float32, error) {
	if len(response.Data) != expectedCount {
		return nil, fmt.Errorf("%w: got %d vectors, want %d", ErrInvalidResponse, len(response.Data), expectedCount)
	}
	ordered := make([][]float32, expectedCount)
	for _, item := range response.Data {
		if item.Index < 0 || item.Index >= expectedCount || ordered[item.Index] != nil {
			return nil, fmt.Errorf("%w: invalid or duplicate index %d", ErrInvalidResponse, item.Index)
		}
		if !validVector(item.Values, dimension) {
			return nil, fmt.Errorf("%w: index %d has dimension %d, want %d", ErrInvalidResponse, item.Index, len(item.Values), dimension)
		}
		ordered[item.Index] = item.Values
	}
	return ordered, nil
}

func validVector(values []float32, dimension int) bool {
	if len(values) != dimension {
		return false
	}
	for _, value := range values {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return false
		}
	}
	return true
}

func retryable(ctx context.Context, err error) bool {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
		return false
	}
	var apiErr *provider.EmbeddingAPIError
	if errors.As(err, &apiErr) {
		return apiErr.Retryable()
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var networkErr net.Error
	return errors.As(err, &networkErr)
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

func (g *gateway) cacheKey(userID, contentHash string) string {
	tenantHash := hashString(userID)
	return hashString(strings.Join([]string{g.config.Provider, g.config.Model, fmt.Sprintf("%d", g.config.Dimension), tenantHash, contentHash}, "\x00"))
}

func hashString(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func cloneVector(values []float32) []float32 {
	return append([]float32(nil), values...)
}

func (g *gateway) observe(ctx context.Context, request Request, result Result, status string, err error, started time.Time) {
	g.observation.Record(ctx, Record{
		OperationID: request.OperationID, Purpose: request.Purpose, UserID: request.UserID,
		Provider: g.config.Provider, Model: g.config.Model, InputCount: len(request.Texts), Dimension: g.config.Dimension,
		EstimatedTokens: result.EstimatedTokens, ActualTokens: result.InputTokens,
		UsageReported: result.UsageReported,
		CacheHits:     result.CacheHits, Deduplicated: result.Deduplicated,
		RequestCount: result.RequestCount, RetryCount: result.RetryCount,
		CostNanoUSD: result.CostNanoUSD, CostKnown: result.CostKnown,
		Duration: time.Since(started), Status: status, Err: err,
	})
}
