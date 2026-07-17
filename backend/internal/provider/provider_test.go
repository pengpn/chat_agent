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

func TestRegistryRejectsAProviderModelThatIsNotEnabled(t *testing.T) {
	registry := NewRegistry([]config.ProviderConfig{{ID: "openai", Type: "openai-compatible", BaseURL: "https://example.com", APIKey: "key", Models: []string{"allowed"}}})
	if _, err := registry.Get("openai", "not-allowed"); err == nil || !strings.Contains(err.Error(), "not enabled") {
		t.Fatalf("expected disabled model error, got %v", err)
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
