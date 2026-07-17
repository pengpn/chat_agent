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
	"time"

	"github.com/example/agent-platform/backend/internal/config"
)

type anthropic struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

func NewAnthropic(cfg config.ProviderConfig) Provider {
	return &anthropic{baseURL: cfg.BaseURL, apiKey: cfg.APIKey, client: &http.Client{Timeout: 10 * time.Minute}}
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
			Model       string    `json:"model"`
			System      string    `json:"system,omitempty"`
			Messages    []Message `json:"messages"`
			MaxTokens   int       `json:"max_tokens"`
			Stream      bool      `json:"stream"`
			Temperature *float64  `json:"temperature,omitempty"`
		}{Model: request.Model, System: system, Messages: messages, MaxTokens: 4096, Stream: true, Temperature: request.Temperature}
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
			sendEvent(ctx, events, StreamEvent{Err: err})
			return
		}
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			message, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
			sendEvent(ctx, events, StreamEvent{Err: fmt.Errorf("Anthropic API returned %s: %s", response.Status, strings.TrimSpace(string(message)))})
			return
		}

		scanner := bufio.NewScanner(response.Body)
		scanner.Buffer(make([]byte, 0, 64<<10), 2<<20)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			var chunk struct {
				Type  string `json:"type"`
				Delta struct {
					Text string `json:"text"`
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
				if chunk.Delta.Text != "" {
					sendEvent(ctx, events, StreamEvent{Delta: chunk.Delta.Text})
				}
			case "message_stop":
				sendEvent(ctx, events, StreamEvent{Done: true})
				return
			case "message_delta":
				usage := TokenUsage{InputTokens: chunk.Usage.InputTokens, OutputTokens: chunk.Usage.OutputTokens}
				usage.TotalTokens = usage.InputTokens + usage.OutputTokens
				sendEvent(ctx, events, StreamEvent{Usage: &usage})
			case "error":
				sendEvent(ctx, events, StreamEvent{Err: fmt.Errorf("Anthropic returned a streaming error")})
				return
			}
		}
		if err := scanner.Err(); err != nil {
			sendEvent(ctx, events, StreamEvent{Err: err})
			return
		}
		sendEvent(ctx, events, StreamEvent{Done: true})
	}()
	return events
}
