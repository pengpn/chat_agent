package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/example/agent-platform/backend/internal/config"
)

func TestOpenAICompatibleStreamsContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("authorization header was not set")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"你好\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"世界\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := &openAICompatible{baseURL: server.URL, apiKey: "test-key", client: server.Client()}
	result := collect(client.ChatStream(context.Background(), ChatRequest{Model: "demo", Messages: []Message{{Role: "user", Content: "hi"}}}))
	if result != "你好世界" {
		t.Fatalf("streamed result = %q, want %q", result, "你好世界")
	}
}

func TestOpenAICompatibleReportsUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			StreamOptions struct {
				IncludeUsage bool `json:"include_usage"`
			} `json:"stream_options"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if !request.StreamOptions.IncludeUsage {
			t.Fatal("stream_options.include_usage was not requested")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":7,\"total_tokens\":19}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := &openAICompatible{baseURL: server.URL, apiKey: "test-key", includeUsage: true, client: server.Client()}
	var usage *TokenUsage
	for event := range client.ChatStream(context.Background(), ChatRequest{Model: "demo", Messages: []Message{{Role: "user", Content: "hi"}}}) {
		if event.Err != nil {
			t.Fatal(event.Err)
		}
		if event.Usage != nil {
			usage = event.Usage
		}
	}
	if usage == nil || usage.InputTokens != 12 || usage.OutputTokens != 7 || usage.TotalTokens != 19 {
		t.Fatalf("usage = %#v", usage)
	}
}

func TestAnthropicSendsSystemSeparatelyAndStreamsText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "test-key" {
			t.Fatalf("x-api-key header was not set")
		}
		var request struct {
			System   string    `json:"system"`
			Messages []Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.System != "be concise" {
			t.Fatalf("system = %q", request.System)
		}
		if len(request.Messages) != 1 || request.Messages[0].Role != "user" {
			t.Fatalf("messages = %#v", request.Messages)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"hello \"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"agent\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer server.Close()

	client := &anthropic{baseURL: server.URL, apiKey: "test-key", client: server.Client()}
	result := collect(client.ChatStream(context.Background(), ChatRequest{Model: "demo", Messages: []Message{{Role: "system", Content: "be concise"}, {Role: "user", Content: "hi"}}}))
	if result != "hello agent" {
		t.Fatalf("streamed result = %q", result)
	}
}

func TestAnthropicReportsUsageAcrossStreamEvents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":5}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer server.Close()

	client := &anthropic{baseURL: server.URL, apiKey: "test-key", client: server.Client()}
	var usage TokenUsage
	for event := range client.ChatStream(context.Background(), ChatRequest{Model: "demo", Messages: []Message{{Role: "user", Content: "hi"}}}) {
		if event.Err != nil {
			t.Fatal(event.Err)
		}
		if event.Usage != nil {
			usage.Merge(*event.Usage)
		}
	}
	if usage.InputTokens != 10 || usage.OutputTokens != 5 || usage.TotalTokens != 15 {
		t.Fatalf("usage = %#v", usage)
	}
}

func TestRegistryRejectsAProviderModelThatIsNotEnabled(t *testing.T) {
	registry := NewRegistry([]config.ProviderConfig{{ID: "openai", Type: "openai-compatible", BaseURL: "https://example.com", APIKey: "key", Models: []string{"allowed"}}})
	if _, err := registry.Get("openai", "not-allowed"); err == nil || !strings.Contains(err.Error(), "not enabled") {
		t.Fatalf("expected disabled model error, got %v", err)
	}
}

func TestRegistryCalculatesCostInNanoUSD(t *testing.T) {
	registry := NewRegistry([]config.ProviderConfig{{
		ID:      "openai",
		Type:    "openai-compatible",
		BaseURL: "https://example.com",
		APIKey:  "key",
		Models:  []string{"demo"},
		ModelPricing: map[string]config.ModelPricing{
			"demo": {InputUSDPerMillionTokens: 0.15, OutputUSDPerMillionTokens: 0.60},
		},
	}})
	cost, known := registry.CalculateCost("openai", "demo", TokenUsage{InputTokens: 1_000, OutputTokens: 2_000, TotalTokens: 3_000})
	if !known {
		t.Fatal("expected configured pricing to be known")
	}
	if cost != 1_350_000 { // USD 0.00135 expressed in nano USD.
		t.Fatalf("cost = %d, want 1350000", cost)
	}
	if _, known := registry.CalculateCost("openai", "unpriced", TokenUsage{}); known {
		t.Fatal("expected unpriced model cost to be unknown")
	}
}

func collect(events <-chan StreamEvent) string {
	var content strings.Builder
	for event := range events {
		if event.Err != nil {
			panic(event.Err)
		}
		content.WriteString(event.Delta)
	}
	return content.String()
}
