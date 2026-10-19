// Package rerankgateway 封装重排模型的预算、超时、结果校验和观测。
package rerankgateway

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/example/agent-platform/backend/internal/config"
	"github.com/example/agent-platform/backend/internal/embeddinggateway"
	"github.com/example/agent-platform/backend/internal/logger"
	"github.com/example/agent-platform/backend/internal/provider"
)

var (
	ErrNotConfigured   = errors.New("RerankGateway 未配置")
	ErrInvalidInput    = errors.New("重排请求无效")
	ErrBudgetExceeded  = errors.New("重排输入超过配置预算")
	ErrInvalidResponse = errors.New("重排模型返回无效结果")
)

type Request struct {
	UserID    string
	Query     string
	Documents []string
}

type Item struct {
	Index int
	Score float64
}

type Result struct {
	Provider          string
	Model             string
	Items             []Item
	EstimatedTokens   int64
	InputTokens       *int64
	BilledSearchUnits *float64
}

type Gateway interface {
	Rerank(context.Context, Request) (Result, error)
}
type Client interface {
	Rerank(context.Context, provider.RerankRequest) (provider.RerankResponse, error)
}

type gateway struct {
	client    Client
	estimator embeddinggateway.TokenEstimator
	config    config.RerankConfig
}

func NewConfigured(cfg config.RerankConfig) (Gateway, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	estimator, err := embeddinggateway.NewTokenEstimator(cfg.Tokenizer)
	if err != nil {
		return nil, err
	}
	return New(provider.NewCompatibleRerank(cfg), estimator, cfg), nil
}

func New(client Client, estimator embeddinggateway.TokenEstimator, cfg config.RerankConfig) Gateway {
	if estimator == nil {
		estimator = embeddinggateway.HeuristicTokenEstimator{}
	}
	if cfg.MaxDocuments <= 0 {
		cfg.MaxDocuments = 60
	}
	if cfg.MaxPairTokens <= 0 {
		cfg.MaxPairTokens = 4096
	}
	if cfg.MaxTotalTokens <= 0 {
		cfg.MaxTotalTokens = 32768
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 15 * time.Second
	}
	return &gateway{client: client, estimator: estimator, config: cfg}
}

func (g *gateway) Rerank(ctx context.Context, request Request) (result Result, err error) {
	started := time.Now()
	result.Provider, result.Model = g.config.Provider, g.config.Model
	defer func() {
		fields := []logger.Field{logger.String("user_id", request.UserID), logger.String("provider", g.config.Provider),
			logger.String("model", result.Model), logger.String("requested_model", g.config.Model), logger.Int("document_count", len(request.Documents)),
			logger.Int64("estimated_tokens", result.EstimatedTokens), logger.Any("input_tokens", result.InputTokens),
			logger.Any("billed_search_units", result.BilledSearchUnits), logger.Int64("duration_ms", time.Since(started).Milliseconds())}
		if err != nil {
			logger.Error(ctx, "rerank request failed", append(fields, logger.Err(err))...)
		} else {
			logger.Info(ctx, "rerank request completed", fields...)
		}
	}()
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if g.client == nil {
		return result, ErrNotConfigured
	}
	if request.UserID == "" || strings.TrimSpace(request.Query) == "" || len(request.Documents) == 0 {
		return result, ErrInvalidInput
	}
	if len(request.Documents) > g.config.MaxDocuments {
		return result, fmt.Errorf("%w: documents=%d limit=%d", ErrBudgetExceeded, len(request.Documents), g.config.MaxDocuments)
	}
	queryTokens := g.estimator.Estimate(request.Query)
	for _, document := range request.Documents {
		if strings.TrimSpace(document) == "" {
			return result, ErrInvalidInput
		}
		pairTokens := queryTokens + g.estimator.Estimate(document)
		if pairTokens > g.config.MaxPairTokens {
			return result, fmt.Errorf("%w: pair_tokens=%d limit=%d", ErrBudgetExceeded, pairTokens, g.config.MaxPairTokens)
		}
		if pairTokens > g.config.MaxTotalTokens-result.EstimatedTokens {
			return result, fmt.Errorf("%w: total token limit=%d", ErrBudgetExceeded, g.config.MaxTotalTokens)
		}
		result.EstimatedTokens += pairTokens
	}
	callCtx, cancel := context.WithTimeout(ctx, g.config.RequestTimeout)
	defer cancel()
	response, err := g.client.Rerank(callCtx, provider.RerankRequest{Model: g.config.Model, Query: request.Query, Documents: request.Documents})
	if err != nil {
		return result, err
	}
	if err := callCtx.Err(); err != nil {
		return result, err
	}
	if response.InputTokens != nil && *response.InputTokens < 0 {
		return result, ErrInvalidResponse
	}
	if response.BilledSearchUnits != nil && (*response.BilledSearchUnits < 0 || math.IsNaN(*response.BilledSearchUnits) || math.IsInf(*response.BilledSearchUnits, 0)) {
		return result, ErrInvalidResponse
	}
	result.InputTokens, result.BilledSearchUnits = response.InputTokens, response.BilledSearchUnits
	if response.Model != "" {
		result.Model = response.Model
	}
	items := make([]Item, len(response.Items))
	for i, item := range response.Items {
		items[i] = Item{Index: item.Index, Score: item.Score}
	}
	ordered, err := Order(items, len(request.Documents))
	if err != nil {
		return result, err
	}
	result.Items = ordered
	return result, nil
}

// Order 要求每个输入位置恰好出现一次，不接受缺项、越界、重复索引或非有限分数。
// 分数相同时保留重排前的候选顺序；分数范围由具体模型定义。
func Order(items []Item, count int) ([]Item, error) {
	if count <= 0 || len(items) != count {
		return nil, fmt.Errorf("%w: incomplete result count", ErrInvalidResponse)
	}
	seen := make(map[int]bool, count)
	ordered := append([]Item(nil), items...)
	for _, item := range ordered {
		if item.Index < 0 || item.Index >= count || seen[item.Index] || math.IsNaN(item.Score) || math.IsInf(item.Score, 0) {
			return nil, ErrInvalidResponse
		}
		seen[item.Index] = true
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Score != ordered[j].Score {
			return ordered[i].Score > ordered[j].Score
		}
		return ordered[i].Index < ordered[j].Index
	})
	return ordered, nil
}
