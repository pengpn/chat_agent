package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/example/agent-platform/backend/internal/provider"
)

// toolSpec 只复用固定定义，不负责按名称分派执行；每个具体 Tool 有独立 Execute。
type toolSpec struct {
	definition provider.ToolDefinition
	metadata   Metadata
	timeout    time.Duration
}

func (s toolSpec) Definition() provider.ToolDefinition { return s.definition }
func (s toolSpec) Metadata() Metadata                  { return s.metadata }
func (s toolSpec) ExecutionTimeout() time.Duration     { return s.timeout }
func newSpec(namespace, name, category, description, schema string, capabilities ...string) toolSpec {
	wireName := namespace + "_" + name
	return toolSpec{definition: provider.ToolDefinition{Type: "function", Function: provider.ToolFunctionDefinition{Name: wireName, Description: description, Parameters: json.RawMessage(schema)}}, metadata: Metadata{Namespace: namespace, Name: name, Category: category, Version: "1.0.0", Source: "builtin", Exposure: "model", WireName: wireName, Capabilities: capabilities}}
}

// executeTyped 统一 JSON → Go 参数 → 类型明确的方法 → JSON 结果。
// Schema 只由 Registry 编译与校验；这里保留严格解码，防止字段静默丢失。
func executeTyped[Input, Output any](ctx context.Context, raw string, execute func(context.Context, Input) (Output, error)) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !strings.HasPrefix(strings.TrimSpace(raw), "{") {
		return "", fmt.Errorf("工具参数必须为 JSON 对象")
	}
	var input Input
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return "", fmt.Errorf("工具参数无效: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return "", fmt.Errorf("只允许一个 JSON 参数对象")
	}
	result, err := execute(ctx, input)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(result)
	return string(encoded), err
}
