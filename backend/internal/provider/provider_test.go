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
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"先理解问题。\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"你好\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"世界\"},\"finish_reason\":\"stop\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := &openAICompatible{baseURL: server.URL, apiKey: "test-key", client: server.Client()}
	var content, think strings.Builder
	finishReason := ""
	for event := range client.ChatStream(context.Background(), ChatRequest{Model: "demo", Messages: []Message{{Role: "user", Content: "hi"}}}) {
		if event.Err != nil {
			t.Fatal(event.Err)
		}
		content.WriteString(event.Delta)
		think.WriteString(event.ThinkDelta)
		if event.Done {
			finishReason = event.FinishReason
		}
	}
	if content.String() != "你好世界" || think.String() != "先理解问题。" || finishReason != "stop" {
		t.Fatalf("content=%q think=%q finishReason=%q", content.String(), think.String(), finishReason)
	}
}

func TestOpenAICompatibleTreatsMissingDoneAsDisconnect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"))
	}))
	defer server.Close()

	client := &openAICompatible{baseURL: server.URL, apiKey: "test-key", client: server.Client()}
	var streamErr error
	for event := range client.ChatStream(context.Background(), ChatRequest{Model: "demo", Messages: []Message{{Role: "user", Content: "hi"}}}) {
		if event.Err != nil {
			streamErr = event.Err
		}
	}
	kind := StreamErrorKindOf(streamErr)
	if kind != StreamErrorDisconnected {
		t.Fatalf("kind=%q err=%v", kind, streamErr)
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

func TestOpenAICompatibleChatReturnsCompleteResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if _, streaming := request["stream"]; streaming {
			t.Fatal("non-streaming chat unexpectedly included stream")
		}
		if request["tool_choice"] != "auto" {
			t.Fatalf("tool_choice = %#v", request["tool_choice"])
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"最终答案","reasoning_content":"先分析问题。","tool_calls":[{"id":"call_1","type":"function","function":{"name":"calculator","arguments":"{\"expression\":\"2+2\"}"}}]}}],"usage":{"prompt_tokens":12,"completion_tokens":7,"total_tokens":19}}`))
	}))
	defer server.Close()

	client := &openAICompatible{baseURL: server.URL, apiKey: "test-key", client: server.Client()}
	result, err := client.Chat(context.Background(), ChatRequest{
		Model:    "demo",
		Messages: []Message{{Role: "user", Content: "hi"}},
		Tools:    []ToolDefinition{{Type: "function", Function: ToolFunctionDefinition{Name: "calculator"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "最终答案" || result.Think != "先分析问题。" || len(result.ToolCalls) != 1 || result.ToolCalls[0].Function.Name != "calculator" {
		t.Fatalf("result = %#v", result)
	}
	if result.Usage == nil || result.Usage.InputTokens != 12 || result.Usage.OutputTokens != 7 || result.Usage.TotalTokens != 19 {
		t.Fatalf("usage = %#v", result.Usage)
	}
}

func TestOpenAIChatSendsNativeJSONSchemaResponseFormat(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ResponseFormat struct {
				Type       string `json:"type"`
				JSONSchema struct {
					Name   string         `json:"name"`
					Strict bool           `json:"strict"`
					Schema map[string]any `json:"schema"`
				} `json:"json_schema"`
			} `json:"response_format"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.ResponseFormat.Type != "json_schema" || request.ResponseFormat.JSONSchema.Name != "answer" || !request.ResponseFormat.JSONSchema.Strict || request.ResponseFormat.JSONSchema.Schema["type"] != "object" {
			t.Fatalf("response_format = %#v", request.ResponseFormat)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"answer\":\"ok\"}"}}]}`))
	}))
	defer server.Close()

	client := &openAICompatible{providerID: "openai", baseURL: server.URL, apiKey: "test-key", client: server.Client()}
	_, err := client.Chat(context.Background(), ChatRequest{
		Model: "demo", Messages: []Message{{Role: "user", Content: "hi"}},
		ResponseFormat: &ResponseFormat{Type: "json_schema", Name: "answer", Strict: true, Schema: json.RawMessage(`{"type":"object"}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDeepSeekChatEnablesThinkingAndOmitsTemperature(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Thinking *struct {
				Type string `json:"type"`
			} `json:"thinking"`
			Temperature *float64 `json:"temperature"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Thinking == nil || request.Thinking.Type != "enabled" {
			t.Fatalf("thinking = %#v", request.Thinking)
		}
		if request.Temperature != nil {
			t.Fatalf("temperature = %v, want omitted", *request.Temperature)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"答案","reasoning_content":"推理"}}]}`))
	}))
	defer server.Close()

	temperature := 0.7
	client := &openAICompatible{providerID: "deepseek", baseURL: server.URL, apiKey: "test-key", client: server.Client()}
	result, err := client.Chat(context.Background(), ChatRequest{
		Model:       "deepseek-v4-flash",
		Messages:    []Message{{Role: "user", Content: "hi"}},
		Thinking:    &ThinkingConfig{Enabled: true, BudgetTokens: 2048},
		Temperature: &temperature,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Think != "推理" {
		t.Fatalf("result = %#v", result)
	}
}

func TestDeepSeekStreamEnablesThinking(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Stream   bool `json:"stream"`
			Thinking *struct {
				Type string `json:"type"`
			} `json:"thinking"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if !request.Stream || request.Thinking == nil || request.Thinking.Type != "enabled" {
			t.Fatalf("request = %#v", request)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := &openAICompatible{providerID: "deepseek", baseURL: server.URL, apiKey: "test-key", client: server.Client()}
	for event := range client.ChatStream(context.Background(), ChatRequest{
		Model:    "deepseek-v4-flash",
		Messages: []Message{{Role: "user", Content: "hi"}},
		Thinking: &ThinkingConfig{Enabled: true, BudgetTokens: 2048},
	}) {
		if event.Err != nil {
			t.Fatal(event.Err)
		}
	}
}

func TestOpenAICompatibleStreamsToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Tools      []ToolDefinition `json:"tools"`
			ToolChoice string           `json:"tool_choice"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.ToolChoice != "auto" || len(request.Tools) != 1 || request.Tools[0].Function.Name != "calculator" {
			t.Fatalf("tool request = %#v", request)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"calculator\",\"arguments\":\"{\\\"expression\\\":\\\"2+\"}}]}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"2\\\"}\"}}]}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := &openAICompatible{baseURL: server.URL, apiKey: "test-key", client: server.Client()}
	events := client.ChatStream(context.Background(), ChatRequest{
		Model:    "demo",
		Messages: []Message{{Role: "user", Content: "calculate 2+2"}},
		Tools:    []ToolDefinition{{Type: "function", Function: ToolFunctionDefinition{Name: "calculator"}}},
	})
	var calls []ToolCall
	for event := range events {
		if event.Err != nil {
			t.Fatal(event.Err)
		}
		calls = append(calls, event.ToolCalls...)
	}
	if len(calls) != 1 {
		t.Fatalf("tool calls = %#v", calls)
	}
	if calls[0].ID != "call_1" || calls[0].Function.Name != "calculator" || calls[0].Function.Arguments != `{"expression":"2+2"}` {
		t.Fatalf("tool call = %#v", calls[0])
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

func TestAnthropicChatReturnsContentAndThinking(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			System      string             `json:"system"`
			Messages    []Message          `json:"messages"`
			Thinking    *anthropicThinking `json:"thinking"`
			Temperature *float64           `json:"temperature"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.System != "be concise" || len(request.Messages) != 1 || request.Messages[0].Role != "user" {
			t.Fatalf("request = %#v", request)
		}
		if request.Thinking == nil || request.Thinking.Type != "enabled" || request.Thinking.BudgetTokens != 2048 || request.Thinking.Display != "summarized" {
			t.Fatalf("thinking = %#v", request.Thinking)
		}
		if request.Temperature != nil {
			t.Fatalf("temperature = %v, want omitted", *request.Temperature)
		}
		_, _ = w.Write([]byte(`{"content":[{"type":"thinking","thinking":"先检查上下文。"},{"type":"text","text":"这是答案。"}],"usage":{"input_tokens":10,"output_tokens":5}}`))
	}))
	defer server.Close()

	client := &anthropic{baseURL: server.URL, apiKey: "test-key", client: server.Client()}
	temperature := 0.7
	result, err := client.Chat(context.Background(), ChatRequest{
		Model:       "demo",
		Messages:    []Message{{Role: "system", Content: "be concise"}, {Role: "user", Content: "hi"}},
		Thinking:    &ThinkingConfig{Enabled: true, BudgetTokens: 2048},
		Temperature: &temperature,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "这是答案。" || result.Think != "先检查上下文。" {
		t.Fatalf("result = %#v", result)
	}
	if result.Usage == nil || result.Usage.InputTokens != 10 || result.Usage.OutputTokens != 5 || result.Usage.TotalTokens != 15 {
		t.Fatalf("usage = %#v", result.Usage)
	}
}

func TestAnthropicChatCombinesSystemMessagesAndUsesRequestedOutputLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			System    string    `json:"system"`
			Messages  []Message `json:"messages"`
			MaxTokens int64     `json:"max_tokens"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.System != "primary system\n\nEarlier conversation summary:\nsummary" {
			t.Fatalf("system = %q", request.System)
		}
		if request.MaxTokens != 768 || len(request.Messages) != 1 || request.Messages[0].Role != "user" {
			t.Fatalf("request = %#v", request)
		}
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	client := &anthropic{baseURL: server.URL, apiKey: "test-key", client: server.Client()}
	_, err := client.Chat(context.Background(), ChatRequest{
		Model: "demo", MaxOutputTokens: 768,
		Messages: []Message{
			{Role: "system", Content: "primary system"},
			{Role: "system", Content: "Earlier conversation summary:\nsummary"},
			{Role: "user", Content: "hello"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRegistrySupportsThinkingOnlyForCompatibleModels(t *testing.T) {
	registry := NewRegistry([]config.ProviderConfig{
		{ID: "openai", Type: "openai-compatible", BaseURL: "https://example.com", APIKey: "key", Models: []string{"gpt-4o"}},
		{ID: "deepseek", Type: "openai-compatible", BaseURL: "https://example.com", APIKey: "key", Models: []string{"deepseek-v4-flash"}},
		{ID: "glm", Type: "openai-compatible", BaseURL: "https://example.com", APIKey: "key", Models: []string{"glm-4.5"}},
		{ID: "anthropic", Type: "anthropic", BaseURL: "https://example.com", APIKey: "key", Models: []string{"claude-sonnet"}},
	})
	for _, candidate := range []struct {
		provider string
		model    string
		want     bool
	}{
		{"openai", "gpt-4o", false},
		{"deepseek", "deepseek-v4-flash", true},
		{"deepseek", "deepseek-chat", false},
		{"glm", "glm-4.5", true},
		{"glm", "glm-4-flash", false},
		{"anthropic", "claude-sonnet", true},
	} {
		if got := registry.SupportsThinking(candidate.provider, candidate.model); got != candidate.want {
			t.Errorf("SupportsThinking(%q, %q) = %t, want %t", candidate.provider, candidate.model, got, candidate.want)
		}
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
