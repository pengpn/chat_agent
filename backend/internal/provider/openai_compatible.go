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

type openAICompatible struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

func NewOpenAICompatible(cfg config.ProviderConfig) Provider {
	return &openAICompatible{baseURL: cfg.BaseURL, apiKey: cfg.APIKey, client: &http.Client{Timeout: 10 * time.Minute}}
}

func (p *openAICompatible) ChatStream(ctx context.Context, request ChatRequest) <-chan StreamEvent {
	events := make(chan StreamEvent)
	go func() {
		defer close(events)
		body := struct {
			Model       string    `json:"model"`
			Messages    []Message `json:"messages"`
			Stream      bool      `json:"stream"`
			Temperature *float64  `json:"temperature,omitempty"`
		}{Model: request.Model, Messages: request.Messages, Stream: true, Temperature: request.Temperature}
		payload, err := json.Marshal(body)
		if err != nil {
			sendEvent(ctx, events, StreamEvent{Err: err})
			return
		}

		httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(payload))
		if err != nil {
			sendEvent(ctx, events, StreamEvent{Err: err})
			return
		}
		httpRequest.Header.Set("Authorization", "Bearer "+p.apiKey)
		httpRequest.Header.Set("Content-Type", "application/json")
		httpRequest.Header.Set("Accept", "text/event-stream")

		response, err := p.client.Do(httpRequest)
		if err != nil {
			sendEvent(ctx, events, StreamEvent{Err: err})
			return
		}
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			message, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
			sendEvent(ctx, events, StreamEvent{Err: fmt.Errorf("model API returned %s: %s", response.Status, strings.TrimSpace(string(message)))})
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
			if data == "[DONE]" {
				sendEvent(ctx, events, StreamEvent{Done: true})
				return
			}
			var chunk struct {
				Choices []struct {
					Delta struct {
						Content string `json:"content"`
					} `json:"delta"`
				} `json:"choices"`
			}
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				continue
			}
			for _, choice := range chunk.Choices {
				if choice.Delta.Content != "" {
					sendEvent(ctx, events, StreamEvent{Delta: choice.Delta.Content})
				}
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

func sendEvent(ctx context.Context, events chan<- StreamEvent, event StreamEvent) {
	select {
	case events <- event:
	case <-ctx.Done():
	}
}
