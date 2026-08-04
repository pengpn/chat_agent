package llmgateway

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/example/agent-platform/backend/internal/provider"
)

func TestDeepSeekV4TokenizerMatchesOfficialVectors(t *testing.T) {
	tokenizer, err := loadDeepSeekV4Tokenizer()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		input string
		want  int64
	}{
		{name: "空文本", input: "", want: 0},
		{name: "英文", input: "hello tokenizer", want: 3},
		{name: "中英文", input: "你好，DeepSeek V4！", want: 8},
		{name: "前导空格", input: "   spaces before word", want: 4},
		{name: "长数字", input: "1234567890", want: 4},
		{name: "连续中文", input: "中文连续文本测试", want: 4},
		{name: "日文", input: "日本語テスト", want: 2},
		{name: "韩文", input: "한국어 테스트", want: 5},
		{name: "表情符号", input: "emoji 😀🚀✨", want: 7},
		{name: "换行", input: "line1\nline2\r\n", want: 7},
		{name: "多段空格", input: "a  b   c", want: 5},
		{name: "末尾空格", input: "  trailing  ", want: 3},
		{name: "代码符号", input: "if (x >= 10) { return true; }", want: 12},
		{name: "思考特殊Token", input: "<think>reason</think>", want: 3},
		{
			name:  "官方消息特殊Token",
			input: deepSeekV4BOS + "You are helpful." + deepSeekV4User + "你好" + deepSeekV4Assistant + deepSeekV4ThinkEnd,
			want:  9,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tokenizer.Count(tt.input)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("Count() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestDeepSeekV4TokenizerHandlesLongInput(t *testing.T) {
	tokenizer, err := loadDeepSeekV4Tokenizer()
	if err != nil {
		t.Fatal(err)
	}
	count, err := tokenizer.Count(strings.Repeat("上下文管理", 2_000))
	if err != nil {
		t.Fatal(err)
	}
	if count <= 0 {
		t.Fatalf("Count() = %d, want positive", count)
	}
}

func TestRenderDeepSeekV4PromptUsesOfficialChatEncoding(t *testing.T) {
	request := provider.ChatRequest{
		Model: "deepseek-v4-flash",
		Messages: []provider.Message{
			{Role: "system", Content: "You are helpful."},
			{Role: "user", Content: "你好"},
		},
	}
	prompt, err := renderDeepSeekV4Prompt(request)
	if err != nil {
		t.Fatal(err)
	}
	want := deepSeekV4BOS + "You are helpful." + deepSeekV4User + "你好" + deepSeekV4Assistant + deepSeekV4ThinkEnd
	if prompt != want {
		t.Fatalf("prompt = %q, want %q", prompt, want)
	}
	if got, err := estimateDeepSeekV4Input(request); err != nil || got != 9 {
		t.Fatalf("estimateDeepSeekV4Input() = %d, %v; want 9, nil", got, err)
	}
}

func TestModelTokenEstimatorUsesDeepSeekOfficialTokenizer(t *testing.T) {
	request := provider.ChatRequest{
		Model: "deepseek-v4-flash",
		Messages: []provider.Message{
			{Role: "system", Content: "You are helpful."},
			{Role: "user", Content: "你好"},
		},
	}
	estimator := NewModelTokenEstimator(fakeCatalog{tokenizerName: deepSeekV4TokenizerName})
	if got := estimator.EstimateInput("deepseek", request); got != 9 {
		t.Fatalf("EstimateInput() = %d, want 9", got)
	}
}

func TestDeepSeekV4ToolPromptMatchesOfficialEncoding(t *testing.T) {
	request := provider.ChatRequest{
		Model: "deepseek-v4-flash",
		Messages: []provider.Message{
			{Role: "system", Content: "You are helpful."},
			{Role: "user", Content: "北京天气怎么样？"},
		},
		Tools: []provider.ToolDefinition{{
			Type: "function",
			Function: provider.ToolFunctionDefinition{
				Name:        "get_weather",
				Description: "Get current weather",
				Parameters:  []byte(`{"type":"object","properties":{"location":{"type":"string"},"unit":{"type":"string","enum":["celsius","fahrenheit"]}},"required":["location"]}`),
			},
		}},
	}
	prompt, err := renderDeepSeekV4Prompt(request)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(prompt))
	if got, want := hex.EncodeToString(digest[:]), "ebd8906486e486cb4636b456f3a210453d3dc804c596bd19e772b6ae65fcff1e"; got != want {
		t.Fatalf("prompt SHA-256 = %s, want %s", got, want)
	}
	if got, err := estimateDeepSeekV4Input(request); err != nil || got != 298 {
		t.Fatalf("estimateDeepSeekV4Input() = %d, %v; want 298, nil", got, err)
	}
}

func TestDeepSeekV4ThinkingHistoryMatchesOfficialEncoding(t *testing.T) {
	request := provider.ChatRequest{
		Model:    "deepseek-v4-flash",
		Thinking: &provider.ThinkingConfig{Enabled: true},
		Messages: []provider.Message{
			{Role: "system", Content: "You are helpful."},
			{Role: "user", Content: "hello"},
			{Role: "assistant", ReasoningContent: "hidden prior thought", Content: "Hi!"},
			{Role: "user", Content: "继续"},
		},
	}
	prompt, err := renderDeepSeekV4Prompt(request)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(prompt))
	if got, want := hex.EncodeToString(digest[:]), "a487b6ac429f0ce2d40066fe93b5ee6c9c6c9c52de0b642f29aea8af70a92eb5"; got != want {
		t.Fatalf("prompt SHA-256 = %s, want %s", got, want)
	}
	if got, err := estimateDeepSeekV4Input(request); err != nil || got != 16 {
		t.Fatalf("estimateDeepSeekV4Input() = %d, %v; want 16, nil", got, err)
	}
}

func TestDeepSeekV4ToolRoundMatchesOfficialEncoding(t *testing.T) {
	request := provider.ChatRequest{
		Model:    "deepseek-v4-flash",
		Thinking: &provider.ThinkingConfig{Enabled: true},
		Messages: []provider.Message{
			{Role: "system", Content: "You are helpful."},
			{Role: "user", Content: "北京天气？"},
			{
				Role:             "assistant",
				ReasoningContent: "I should call the tool.",
				ToolCalls: []provider.ToolCall{{
					ID: "call_1", Type: "function",
					Function: provider.ToolFunctionCall{Name: "get_weather", Arguments: `{"location":"Beijing"}`},
				}},
			},
			{Role: "tool", ToolCallID: "call_1", Content: `{"temp":22}`},
		},
		Tools: []provider.ToolDefinition{{
			Type: "function",
			Function: provider.ToolFunctionDefinition{
				Name:        "get_weather",
				Description: "Get current weather",
				Parameters:  []byte(`{"type":"object","properties":{"location":{"type":"string"}},"required":["location"]}`),
			},
		}},
	}
	prompt, err := renderDeepSeekV4Prompt(request)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(prompt))
	if got, want := hex.EncodeToString(digest[:]), "cfd0692881a2078ae7171b86b7e281ad2d052c1d7290749d368dc89ea5f06eab"; got != want {
		t.Fatalf("prompt SHA-256 = %s, want %s", got, want)
	}
	if got, err := estimateDeepSeekV4Input(request); err != nil || got != 344 {
		t.Fatalf("estimateDeepSeekV4Input() = %d, %v; want 344, nil", got, err)
	}
}
