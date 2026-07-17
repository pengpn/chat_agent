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
	providerID   string
	baseURL      string
	apiKey       string
	includeUsage bool
	client       *http.Client
}

type openAIThinking struct {
	Type string `json:"type"`
}

func NewOpenAICompatible(cfg config.ProviderConfig) Provider {
	return &openAICompatible{providerID: cfg.ID, baseURL: cfg.BaseURL, apiKey: cfg.APIKey, includeUsage: cfg.StreamIncludeUsage, client: &http.Client{Timeout: 10 * time.Minute}}
}

func (p *openAICompatible) Chat(ctx context.Context, request ChatRequest) (ChatResponse, error) {
	body := struct {
		Model       string           `json:"model"`
		Messages    []Message        `json:"messages"`
		Tools       []ToolDefinition `json:"tools,omitempty"`
		ToolChoice  string           `json:"tool_choice,omitempty"`
		Thinking    *openAIThinking  `json:"thinking,omitempty"`
		Temperature *float64         `json:"temperature,omitempty"`
	}{Model: request.Model, Messages: request.Messages, Tools: request.Tools, Temperature: request.Temperature}
	if len(request.Tools) > 0 {
		body.ToolChoice = "auto"
	}
	body.Thinking = p.thinkingRequest(request)
	if body.Thinking != nil && p.providerID == "deepseek" {
		// DeepSeek ignores temperature in thinking mode; omit it to make the
		// request intent explicit and portable across its API versions.
		body.Temperature = nil
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return ChatResponse{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return ChatResponse{}, err
	}
	httpRequest.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpRequest.Header.Set("Content-Type", "application/json")

	response, err := p.client.Do(httpRequest)
	if err != nil {
		return ChatResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		return ChatResponse{}, fmt.Errorf("model API returned %s: %s", response.Status, strings.TrimSpace(string(message)))
	}
	var result struct {
		Choices []struct {
			Message struct {
				Content          string     `json:"content"`
				ReasoningContent string     `json:"reasoning_content"`
				Reasoning        string     `json:"reasoning"`
				ToolCalls        []ToolCall `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
			TotalTokens      int64 `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return ChatResponse{}, err
	}
	if len(result.Choices) == 0 {
		return ChatResponse{}, fmt.Errorf("model API returned no choices")
	}
	message := result.Choices[0].Message
	think := message.ReasoningContent
	if think == "" {
		think = message.Reasoning
	}
	chatResponse := ChatResponse{Content: message.Content, Think: think, ToolCalls: message.ToolCalls}
	if result.Usage != nil {
		chatResponse.Usage = &TokenUsage{InputTokens: result.Usage.PromptTokens, OutputTokens: result.Usage.CompletionTokens, TotalTokens: result.Usage.TotalTokens}
	}
	return chatResponse, nil
}

func (p *openAICompatible) ChatStream(ctx context.Context, request ChatRequest) <-chan StreamEvent {
	events := make(chan StreamEvent)
	go func() {
		defer close(events)
		body := struct {
			Model         string    `json:"model"`
			Messages      []Message `json:"messages"`
			Stream        bool      `json:"stream"`
			StreamOptions *struct {
				IncludeUsage bool `json:"include_usage"`
			} `json:"stream_options,omitempty"`
			Tools       []ToolDefinition `json:"tools,omitempty"`
			ToolChoice  string           `json:"tool_choice,omitempty"`
			Thinking    *openAIThinking  `json:"thinking,omitempty"`
			Temperature *float64         `json:"temperature,omitempty"`
		}{Model: request.Model, Messages: request.Messages, Stream: true, Tools: request.Tools, Temperature: request.Temperature}
		if len(request.Tools) > 0 {
			body.ToolChoice = "auto"
		}
		body.Thinking = p.thinkingRequest(request)
		if body.Thinking != nil && p.providerID == "deepseek" {
			body.Temperature = nil
		}
		if p.includeUsage {
			body.StreamOptions = &struct {
				IncludeUsage bool `json:"include_usage"`
			}{IncludeUsage: true}
		}
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

		toolCalls := make(map[int]*ToolCall)
		toolCallOrder := make([]int, 0)
		emitDone := func() {
			if len(toolCallOrder) > 0 {
				calls := make([]ToolCall, 0, len(toolCallOrder))
				for _, index := range toolCallOrder {
					calls = append(calls, *toolCalls[index])
				}
				sendEvent(ctx, events, StreamEvent{ToolCalls: calls})
			}
			sendEvent(ctx, events, StreamEvent{Done: true})
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
				emitDone()
				return
			}
			var chunk struct {
				Choices []struct {
					Delta struct {
						Content          string `json:"content"`
						ReasoningContent string `json:"reasoning_content"`
						Reasoning        string `json:"reasoning"`
						ToolCalls        []struct {
							Index    int              `json:"index"`
							ID       string           `json:"id"`
							Type     string           `json:"type"`
							Function ToolFunctionCall `json:"function"`
						} `json:"tool_calls"`
					} `json:"delta"`
				} `json:"choices"`
				Usage *struct {
					PromptTokens     int64 `json:"prompt_tokens"`
					CompletionTokens int64 `json:"completion_tokens"`
					TotalTokens      int64 `json:"total_tokens"`
				} `json:"usage"`
			}
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				continue
			}
			if chunk.Usage != nil {
				sendEvent(ctx, events, StreamEvent{Usage: &TokenUsage{
					InputTokens:  chunk.Usage.PromptTokens,
					OutputTokens: chunk.Usage.CompletionTokens,
					TotalTokens:  chunk.Usage.TotalTokens,
				}})
			}
			for _, choice := range chunk.Choices {
				think := choice.Delta.ReasoningContent
				if think == "" {
					think = choice.Delta.Reasoning
				}
				if think != "" {
					sendEvent(ctx, events, StreamEvent{ThinkDelta: think})
				}
				if choice.Delta.Content != "" {
					sendEvent(ctx, events, StreamEvent{Delta: choice.Delta.Content})
				}
				for _, partial := range choice.Delta.ToolCalls {
					call, exists := toolCalls[partial.Index]
					if !exists {
						call = &ToolCall{Type: "function"}
						toolCalls[partial.Index] = call
						toolCallOrder = append(toolCallOrder, partial.Index)
					}
					if partial.ID != "" {
						call.ID = partial.ID
					}
					if partial.Type != "" {
						call.Type = partial.Type
					}
					if partial.Function.Name != "" {
						call.Function.Name = partial.Function.Name
					}
					call.Function.Arguments += partial.Function.Arguments
				}
			}
		}
		if err := scanner.Err(); err != nil {
			sendEvent(ctx, events, StreamEvent{Err: err})
			return
		}
		emitDone()
	}()
	return events
}

func (p *openAICompatible) thinkingRequest(request ChatRequest) *openAIThinking {
	if request.Thinking == nil || !request.Thinking.Enabled {
		return nil
	}
	if p.providerID != "deepseek" && p.providerID != "glm" {
		return nil
	}
	return &openAIThinking{Type: "enabled"}
}

func sendEvent(ctx context.Context, events chan<- StreamEvent, event StreamEvent) {
	select {
	case events <- event:
	case <-ctx.Done():
	}
}
