package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/example/agent-platform/backend/internal/config"
)

func TestOpenAICompatibleEmbeddingRequestAndResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/embeddings" || request.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("request path=%q authorization=%q", request.URL.Path, request.Header.Get("Authorization"))
		}
		var body struct {
			Model          string   `json:"model"`
			Input          []string `json:"input"`
			Dimensions     int      `json:"dimensions"`
			EncodingFormat string   `json:"encoding_format"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != "embedding-demo" || len(body.Input) != 2 || body.Dimensions != 3 || body.EncodingFormat != "float" {
			t.Fatalf("body = %#v", body)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"object":"list","model":"embedding-demo","data":[{"index":1,"embedding":[0,1,0]},{"index":0,"embedding":[1,0,0]}],"usage":{"prompt_tokens":7,"total_tokens":7}}`))
	}))
	defer server.Close()

	client := NewOpenAICompatibleEmbedding(config.EmbeddingConfig{
		BaseURL: server.URL + "/v1", APIKey: "test-key", SendDimensions: true, ConnectTimeout: time.Second,
	})
	response, err := client.Embed(context.Background(), EmbeddingRequest{Model: "embedding-demo", Input: []string{"one", "two"}, Dimensions: 3})
	if err != nil {
		t.Fatal(err)
	}
	if response.Model != "embedding-demo" || len(response.Data) != 2 || response.Data[0].Index != 1 || response.Usage == nil || response.Usage.InputTokens != 7 {
		t.Fatalf("response = %#v", response)
	}
}

func TestOpenAICompatibleEmbeddingCanOmitDimensions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if _, exists := body["dimensions"]; exists {
			t.Fatalf("dimensions should be omitted: %#v", body)
		}
		_, _ = writer.Write([]byte(`{"data":[{"index":0,"embedding":[1,0]}]}`))
	}))
	defer server.Close()
	client := NewOpenAICompatibleEmbedding(config.EmbeddingConfig{BaseURL: server.URL, APIKey: "key", SendDimensions: false})
	if _, err := client.Embed(context.Background(), EmbeddingRequest{Model: "demo", Input: []string{"one"}, Dimensions: 2}); err != nil {
		t.Fatal(err)
	}
}

func TestEmbeddingAPIErrorPreservesRetryPolicy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Retry-After", "2")
		http.Error(writer, "slow down", http.StatusTooManyRequests)
	}))
	defer server.Close()
	client := NewOpenAICompatibleEmbedding(config.EmbeddingConfig{BaseURL: server.URL, APIKey: "key"})
	_, err := client.Embed(context.Background(), EmbeddingRequest{Model: "demo", Input: []string{"one"}})
	var apiErr *EmbeddingAPIError
	if !errors.As(err, &apiErr) || !apiErr.Retryable() || apiErr.RetryAfter != 2*time.Second {
		t.Fatalf("error = %#v", err)
	}
}
