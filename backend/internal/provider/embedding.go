package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/example/agent-platform/backend/internal/config"
)

// EmbeddingRequest is the provider-neutral request used by EmbeddingGateway.
// Dimensions is zero when the compatible endpoint should choose its native
// dimension instead of receiving the optional OpenAI dimensions field.
type EmbeddingRequest struct {
	Model      string
	Input      []string
	Dimensions int
}

type Embedding struct {
	Index  int
	Values []float32
}

type EmbeddingUsage struct {
	InputTokens int64
	TotalTokens int64
}

type EmbeddingResponse struct {
	Model string
	Data  []Embedding
	Usage *EmbeddingUsage
}

// EmbeddingAPIError preserves the upstream status and Retry-After hint so the
// gateway can retry only safe, transient failures without parsing error text.
type EmbeddingAPIError struct {
	StatusCode int
	Status     string
	Message    string
	RetryAfter time.Duration
}

func (e *EmbeddingAPIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("embedding API returned %s", e.Status)
	}
	return fmt.Sprintf("embedding API returned %s: %s", e.Status, e.Message)
}

func (e *EmbeddingAPIError) Retryable() bool {
	if e == nil {
		return false
	}
	return e.StatusCode == http.StatusRequestTimeout || e.StatusCode == http.StatusTooManyRequests || e.StatusCode >= 500
}

// OpenAICompatibleEmbeddingClient adapts POST /embeddings to provider-neutral
// values. Batching, validation, retry and cache policy remain in the gateway.
type OpenAICompatibleEmbeddingClient struct {
	baseURL        string
	apiKey         string
	sendDimensions bool
	client         *http.Client
}

func NewOpenAICompatibleEmbedding(cfg config.EmbeddingConfig) *OpenAICompatibleEmbeddingClient {
	return &OpenAICompatibleEmbeddingClient{
		baseURL: strings.TrimRight(cfg.BaseURL, "/"), apiKey: cfg.APIKey,
		sendDimensions: cfg.SendDimensions, client: newHTTPClient(cfg.ConnectTimeout),
	}
}

func (p *OpenAICompatibleEmbeddingClient) Embed(ctx context.Context, request EmbeddingRequest) (EmbeddingResponse, error) {
	body := struct {
		Model          string   `json:"model"`
		Input          []string `json:"input"`
		Dimensions     int      `json:"dimensions,omitempty"`
		EncodingFormat string   `json:"encoding_format"`
	}{Model: request.Model, Input: request.Input, EncodingFormat: "float"}
	if p.sendDimensions {
		body.Dimensions = request.Dimensions
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return EmbeddingResponse{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/embeddings", bytes.NewReader(payload))
	if err != nil {
		return EmbeddingResponse{}, err
	}
	httpRequest.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpRequest.Header.Set("Content-Type", "application/json")
	response, err := p.client.Do(httpRequest)
	if err != nil {
		return EmbeddingResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		return EmbeddingResponse{}, &EmbeddingAPIError{
			StatusCode: response.StatusCode, Status: response.Status,
			Message: strings.TrimSpace(string(message)), RetryAfter: parseRetryAfter(response.Header.Get("Retry-After"), time.Now()),
		}
	}
	var result struct {
		Model string `json:"model"`
		Data  []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
		Usage *struct {
			PromptTokens int64 `json:"prompt_tokens"`
			TotalTokens  int64 `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return EmbeddingResponse{}, fmt.Errorf("decode embedding response: %w", err)
	}
	converted := EmbeddingResponse{Model: result.Model, Data: make([]Embedding, len(result.Data))}
	for index, item := range result.Data {
		converted.Data[index] = Embedding{Index: item.Index, Values: item.Embedding}
	}
	if result.Usage != nil {
		inputTokens := result.Usage.PromptTokens
		if inputTokens == 0 {
			inputTokens = result.Usage.TotalTokens
		}
		converted.Usage = &EmbeddingUsage{InputTokens: inputTokens, TotalTokens: result.Usage.TotalTokens}
	}
	return converted, nil
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if parsed, err := http.ParseTime(value); err == nil && parsed.After(now) {
		return parsed.Sub(now)
	}
	return 0
}
