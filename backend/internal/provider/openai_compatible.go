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
	return &openAICompatible{providerID: cfg.ID, baseURL: cfg.BaseURL, apiKey: cfg.APIKey, includeUsage: cfg.StreamIncludeUsage, client: newHTTPClient(cfg.ConnectTimeout)}
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

// 流式响应层，负责将模型响应转换为流式事件，并发送给业务编排层。
// <-chan StreamEvent 表示调用者只能从 Channel 中读取事件，不能向里面写
func (p *openAICompatible) ChatStream(ctx context.Context, request ChatRequest) <-chan StreamEvent {
	events := make(chan StreamEvent)
	// 这里必须开 Goroutine，因为 HTTP 流会持续一段时间。如果直接读取完整响应后再返回，就失去流式效果。
	go func() {
		defer close(events)

		// 调用模型并解析 SSE
		// 构建厂商请求
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
			sendEvent(ctx, events, StreamEvent{Err: ClassifyConnectError(ctx, err)})
			return
		}
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			message, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
			err := fmt.Errorf("model API returned %s: %s", response.Status, strings.TrimSpace(string(message)))
			sendEvent(ctx, events, StreamEvent{Err: NewStreamError(StreamErrorUpstream, err)})
			return
		}
		sendEvent(ctx, events, StreamEvent{Connected: true})

		// 流式响应中的工具参数可能被拆成多段
		toolCalls := make(map[int]*ToolCall)
		toolCallOrder := make([]int, 0)
		finishReason := ""
		emitDone := func() {
			if len(toolCallOrder) > 0 {
				calls := make([]ToolCall, 0, len(toolCallOrder))
				for _, index := range toolCallOrder {
					calls = append(calls, *toolCalls[index])
				}
				sendEvent(ctx, events, StreamEvent{ToolCalls: calls})
			}
			sendEvent(ctx, events, StreamEvent{Done: true, FinishReason: finishReason})
		}

		scanner := bufio.NewScanner(response.Body)
		scanner.Buffer(make([]byte, 0, 64<<10), 2<<20)
		// 读取模型 SSE
		// 返回原始内容大致是
		/*
			data: {"choices":[{"delta":{"reasoning_content":"先分析"}}]}

			data: {"choices":[{"delta":{"content":"答案是"}}]}

			data: {"choices":[{"delta":{"content":"4"}}]}

			data: [DONE]
		*/
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

			// 当模型返回 [DONE] 时，Provider 才发送完整的 ToolCalls。
			if data == "[DONE]" {
				emitDone()
				return
			}
			var chunk struct {
				Choices []struct {
					FinishReason string `json:"finish_reason"`
					Delta        struct {
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

			//sendEvent:将厂商字段转换为统一事件：
			// 这样上层 Service 不需要知道 DeepSeek 使用 reasoning_content，还是 Anthropic 使用 thinking_delta
			if chunk.Usage != nil {
				sendEvent(ctx, events, StreamEvent{Usage: &TokenUsage{
					InputTokens:  chunk.Usage.PromptTokens,
					OutputTokens: chunk.Usage.CompletionTokens,
					TotalTokens:  chunk.Usage.TotalTokens,
				}})
			}
			for _, choice := range chunk.Choices {
				if choice.FinishReason != "" {
					finishReason = choice.FinishReason
				}
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
					// 不断追加参数
					// 第一段: {"expression":"2+
					// 第二段: 2"}
					/* 最终得到
					{
						"id": "call_1",
						"function": {
							"name": "calculator",
							"arguments": "{\"expression\":\"2+2\"}"
						}
						}
					*/
					call.Function.Arguments += partial.Function.Arguments
				}
			}
		}
		if err := scanner.Err(); err != nil {
			sendEvent(ctx, events, StreamEvent{Err: ClassifyStreamError(ctx, err)})
			return
		}
		// A clean TCP EOF is still an interrupted stream unless the protocol's
		// explicit [DONE] marker was received.
		sendEvent(ctx, events, StreamEvent{Err: NewStreamError(StreamErrorDisconnected, io.ErrUnexpectedEOF)})
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
