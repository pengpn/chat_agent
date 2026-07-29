package llmgateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/example/agent-platform/backend/internal/provider"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

const maxSchemaBytes = 64 << 10

var responseFormatNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func compileResponseFormat(format *provider.ResponseFormat) (*jsonschema.Schema, error) {
	if format == nil {
		return nil, nil
	}
	if format.Type != "json_schema" {
		return nil, fmt.Errorf("%w: type 仅支持 json_schema", ErrInvalidResponseFormat)
	}
	if !responseFormatNamePattern.MatchString(format.Name) {
		return nil, fmt.Errorf("%w: name 必须由 1 到 64 个字母、数字、下划线或连字符组成", ErrInvalidResponseFormat)
	}
	if len(format.Schema) == 0 || len(format.Schema) > maxSchemaBytes {
		return nil, fmt.Errorf("%w: schema 不能为空且不能超过 64 KiB", ErrInvalidResponseFormat)
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(format.Schema))
	if err != nil {
		return nil, fmt.Errorf("%w: schema 不是有效 JSON: %v", ErrInvalidResponseFormat, err)
	}
	root, ok := document.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: schema 根节点必须是对象", ErrInvalidResponseFormat)
	}
	if err := rejectRemoteReferences(root); err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("urn:agent-platform:response-schema", document); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidResponseFormat, err)
	}
	compiled, err := compiler.Compile("urn:agent-platform:response-schema")
	if err != nil {
		return nil, fmt.Errorf("%w: schema 编译失败: %v", ErrInvalidResponseFormat, err)
	}
	return compiled, nil
}

func rejectRemoteReferences(value any) error {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if key == "$ref" {
				reference, ok := child.(string)
				if !ok || !strings.HasPrefix(reference, "#") {
					return fmt.Errorf("%w: 只允许 schema 内部的 $ref", ErrInvalidResponseFormat)
				}
			}
			if err := rejectRemoteReferences(child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range typed {
			if err := rejectRemoteReferences(child); err != nil {
				return err
			}
		}
	}
	return nil
}

func prepareStructuredRequest(request Request) (Request, *jsonschema.Schema, error) {
	compiled, err := compileResponseFormat(request.ChatRequest.ResponseFormat)
	if err != nil || compiled == nil {
		return request, compiled, err
	}
	format := request.ChatRequest.ResponseFormat
	instruction := "你的最终回答必须只包含一个符合以下 JSON Schema 的 JSON 值，不要使用 Markdown 代码块，也不要添加解释文字。\nJSON Schema:\n" + string(format.Schema)
	request.ChatRequest.Messages = append([]provider.Message{{Role: "system", Content: instruction}}, request.ChatRequest.Messages...)
	return request, compiled, nil
}

func validateStructuredContent(schema *jsonschema.Schema, content string) (json.RawMessage, error) {
	if schema == nil {
		return nil, nil
	}
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("%w: 返回内容不是有效 JSON: %v", ErrStructuredOutputInvalid, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("%w: 返回内容包含多个 JSON 值", ErrStructuredOutputInvalid)
		}
		return nil, fmt.Errorf("%w: JSON 后存在额外内容: %v", ErrStructuredOutputInvalid, err)
	}
	if err := schema.Validate(value); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrStructuredOutputInvalid, err)
	}
	normalized, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrStructuredOutputInvalid, err)
	}
	return normalized, nil
}
