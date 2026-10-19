package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/example/agent-platform/backend/internal/config"
)

type RerankRequest struct {
	Model     string
	Query     string
	Documents []string
}

type RerankItem struct {
	Index int     `json:"index"`
	Score float64 `json:"relevance_score"`
}

type RerankResponse struct {
	Model             string
	Items             []RerankItem
	InputTokens       *int64
	BilledSearchUnits *float64
}

// CompatibleRerankClient 适配 model/query/documents/top_n 形式的重排接口。
// 供应商只返回输入位置与相关性分数，业务层始终使用自己的原始片段。
type CompatibleRerankClient struct {
	endpoint string
	apiKey   string
	client   *http.Client
}

func NewCompatibleRerank(cfg config.RerankConfig) *CompatibleRerankClient {
	return &CompatibleRerankClient{endpoint: cfg.Endpoint, apiKey: cfg.APIKey, client: newHTTPClient(cfg.ConnectTimeout)}
}

func (c *CompatibleRerankClient) Rerank(ctx context.Context, request RerankRequest) (RerankResponse, error) {
	payload, err := json.Marshal(struct {
		Model     string   `json:"model"`
		Query     string   `json:"query"`
		Documents []string `json:"documents"`
		TopN      int      `json:"top_n"`
	}{request.Model, request.Query, request.Documents, len(request.Documents)})
	if err != nil {
		return RerankResponse{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return RerankResponse{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	response, err := c.client.Do(httpRequest)
	if err != nil {
		return RerankResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return RerankResponse{}, fmt.Errorf("rerank API returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	if err != nil {
		return RerankResponse{}, err
	}
	if len(data) > 2<<20 {
		return RerankResponse{}, fmt.Errorf("rerank response exceeds size limit")
	}
	var decoded struct {
		Model   string `json:"model"`
		Results []struct {
			Index *int     `json:"index"`
			Score *float64 `json:"relevance_score"`
		} `json:"results"`
		Usage *struct {
			TotalTokens *int64 `json:"total_tokens"`
		} `json:"usage"`
		Meta *struct {
			BilledUnits *struct {
				SearchUnits *float64 `json:"search_units"`
			} `json:"billed_units"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return RerankResponse{}, fmt.Errorf("decode rerank response: %w", err)
	}
	result := RerankResponse{Model: decoded.Model, Items: make([]RerankItem, len(decoded.Results))}
	for i, item := range decoded.Results {
		if item.Index == nil || item.Score == nil {
			return RerankResponse{}, fmt.Errorf("rerank result missing index or relevance_score")
		}
		result.Items[i] = RerankItem{Index: *item.Index, Score: *item.Score}
	}
	if decoded.Usage != nil {
		result.InputTokens = decoded.Usage.TotalTokens
	}
	if decoded.Meta != nil && decoded.Meta.BilledUnits != nil {
		result.BilledSearchUnits = decoded.Meta.BilledUnits.SearchUnits
	}
	return result, nil
}
