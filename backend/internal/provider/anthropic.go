package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/example/agent-platform/backend/internal/config"
)

type anthropic struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

const anthropicMaxTokens = 4096

type anthropicThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens"`
	Display      string `json:"display"`
}

func NewAnthropic(cfg config.ProviderConfig) Provider {
	return &anthropic{baseURL: cfg.BaseURL, apiKey: cfg.APIKey, client: newHTTPClient(cfg.ConnectTimeout)}
}

func (p *anthropic) Chat(ctx context.Context, request ChatRequest) (ChatResponse, error) {
	var system string
	messages := make([]Message, 0, len(request.Messages))
	for _, message := range request.Messages {
		if message.Role == "system" {
			system = message.Content
			continue
		}
		messages = append(messages, message)
	}
	body := struct {
		Model       string             `json:"model"`
		System      string             `json:"system,omitempty"`
		Messages    []Message          `json:"messages"`
		MaxTokens   int                `json:"max_tokens"`
		Thinking    *anthropicThinking `json:"thinking,omitempty"`
		Temperature *float64           `json:"temperature,omitempty"`
	}{Model: request.Model, System: system, Messages: messages, MaxTokens: anthropicMaxTokens, Temperature: request.Temperature}
	body.Thinking = anthropicThinkingRequest(request)
	if body.Thinking != nil {
		// Extended thinking is incompatible with a custom temperature.
		body.Temperature = nil
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return ChatResponse{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/messages", bytes.NewReader(payload))
	if err != nil {
		return ChatResponse{}, err
	}
	httpRequest.Header.Set("x-api-key", p.apiKey)
	httpRequest.Header.Set("anthropic-version", "2023-06-01")
	httpRequest.Header.Set("content-type", "application/json")
	response, err := p.client.Do(httpRequest)
	if err != nil {
		return ChatResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		return ChatResponse{}, fmt.Errorf("Anthropic API returned %s: %s", response.Status, strings.TrimSpace(string(message)))
	}
	var result struct {
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Thinking string `json:"thinking"`
		} `json:"content"`
		Usage struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return ChatResponse{}, err
	}
	var content, think strings.Builder
	for _, block := range result.Content {
		switch block.Type {
		case "text":
			content.WriteString(block.Text)
		case "thinking":
			think.WriteString(block.Thinking)
		}
	}
	usage := &TokenUsage{InputTokens: result.Usage.InputTokens, OutputTokens: result.Usage.OutputTokens}
	usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	return ChatResponse{Content: content.String(), Think: think.String(), Usage: usage}, nil
}

func (p *anthropic) ChatStream(ctx context.Context, request ChatRequest) <-chan StreamEvent {
	events := make(chan StreamEvent)
	go func() {
		defer close(events)
		var system string
		messages := make([]Message, 0, len(request.Messages))
		for _, message := range request.Messages {
			if message.Role == "system" {
				system = message.Content
				continue
			}
			messages = append(messages, message)
		}
		body := struct {
			Model       string             `json:"model"`
			System      string             `json:"system,omitempty"`
			Messages    []Message          `json:"messages"`
			MaxTokens   int                `json:"max_tokens"`
			Stream      bool               `json:"stream"`
			Thinking    *anthropicThinking `json:"thinking,omitempty"`
			Temperature *float64           `json:"temperature,omitempty"`
		}{Model: request.Model, System: system, Messages: messages, MaxTokens: anthropicMaxTokens, Stream: true, Temperature: request.Temperature}
		body.Thinking = anthropicThinkingRequest(request)
		if body.Thinking != nil {
			body.Temperature = nil
		}
		payload, err := json.Marshal(body)
		if err != nil {
			sendEvent(ctx, events, StreamEvent{Err: err})
			return
		}

		httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/messages", bytes.NewReader(payload))
		if err != nil {
			sendEvent(ctx, events, StreamEvent{Err: err})
			return
		}
		httpRequest.Header.Set("x-api-key", p.apiKey)
		httpRequest.Header.Set("anthropic-version", "2023-06-01")
		httpRequest.Header.Set("content-type", "application/json")
		httpRequest.Header.Set("accept", "text/event-stream")

		response, err := p.client.Do(httpRequest)
		if err != nil {
			sendEvent(ctx, events, StreamEvent{Err: ClassifyConnectError(ctx, err)})
			return
		}
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			message, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
			err := fmt.Errorf("Anthropic API returned %s: %s", response.Status, strings.TrimSpace(string(message)))
			sendEvent(ctx, events, StreamEvent{Err: NewStreamError(StreamErrorUpstream, err)})
			return
		}
		sendEvent(ctx, events, StreamEvent{Connected: true})

		scanner := bufio.NewScanner(response.Body)
		scanner.Buffer(make([]byte, 0, 64<<10), 2<<20)
		finishReason := ""
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "" {
				continue
			}
			sendEvent(ctx, events, StreamEvent{Activity: true})
			var chunk struct {
				Type  string `json:"type"`
				Delta struct {
					Type       string `json:"type"`
					Text       string `json:"text"`
					Thinking   string `json:"thinking"`
					StopReason string `json:"stop_reason"`
				} `json:"delta"`
				Message struct {
					Usage struct {
						InputTokens  int64 `json:"input_tokens"`
						OutputTokens int64 `json:"output_tokens"`
					} `json:"usage"`
				} `json:"message"`
				Usage struct {
					InputTokens  int64 `json:"input_tokens"`
					OutputTokens int64 `json:"output_tokens"`
				} `json:"usage"`
			}
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				continue
			}
			switch chunk.Type {
			case "message_start":
				usage := TokenUsage{InputTokens: chunk.Message.Usage.InputTokens, OutputTokens: chunk.Message.Usage.OutputTokens}
				usage.TotalTokens = usage.InputTokens + usage.OutputTokens
				sendEvent(ctx, events, StreamEvent{Usage: &usage})
			case "content_block_delta":
				if chunk.Delta.Type == "thinking_delta" && chunk.Delta.Thinking != "" {
					sendEvent(ctx, events, StreamEvent{ThinkDelta: chunk.Delta.Thinking})
				} else if chunk.Delta.Text != "" {
					sendEvent(ctx, events, StreamEvent{Delta: chunk.Delta.Text})
				}
			case "message_stop":
				sendEvent(ctx, events, StreamEvent{Done: true, FinishReason: finishReason})
				return
			case "message_delta":
				if chunk.Delta.StopReason != "" {
					finishReason = chunk.Delta.StopReason
				}
				usage := TokenUsage{InputTokens: chunk.Usage.InputTokens, OutputTokens: chunk.Usage.OutputTokens}
				usage.TotalTokens = usage.InputTokens + usage.OutputTokens
				sendEvent(ctx, events, StreamEvent{Usage: &usage})
			case "error":
				sendEvent(ctx, events, StreamEvent{Err: fmt.Errorf("Anthropic returned a streaming error")})
				return
			}
		}
		if err := scanner.Err(); err != nil {
			sendEvent(ctx, events, StreamEvent{Err: ClassifyStreamError(ctx, err)})
			return
		}
		sendEvent(ctx, events, StreamEvent{Err: NewStreamError(StreamErrorDisconnected, io.ErrUnexpectedEOF)})
	}()
	return events
}

func anthropicThinkingRequest(request ChatRequest) *anthropicThinking {
	if request.Thinking == nil || !request.Thinking.Enabled {
		return nil
	}
	return &anthropicThinking{
		Type:         "enabled",
		BudgetTokens: request.Thinking.BudgetTokens,
		// Explicitly request the summarized output because the product displays
		// Think content to the user; some current Claude models default to omit it.
		Display: "summarized",
	}
}
