// Package outputformat owns the server-side structured-output contracts that
// product clients may select by ID. Browsers never provide the underlying JSON Schema.
package outputformat

import (
	"encoding/json"
	"sort"

	"github.com/example/agent-platform/backend/internal/provider"
)

type Definition struct {
	ID          string
	Label       string
	Description string
	Format      provider.ResponseFormat
}

type Registry struct {
	definitions map[string]Definition
}

func NewDefaultRegistry() *Registry {
	return NewRegistry([]Definition{
		{
			ID: "contact_extraction", Label: "联系人提取", Description: "从文本中提取姓名、邮箱和电话",
			Format: provider.ResponseFormat{
				Type: "json_schema", Name: "contact_extraction", Description: "从文本中提取的联系人信息", Strict: true,
				Schema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"},"email":{"type":"string"},"phone":{"type":"string"}},"required":["name","email","phone"],"additionalProperties":false}`),
			},
		},
		{
			ID: "task_list", Label: "任务清单", Description: "将内容整理为结构化任务清单",
			Format: provider.ResponseFormat{
				Type: "json_schema", Name: "task_list", Description: "结构化任务清单", Strict: true,
				Schema: json.RawMessage(`{"type":"object","properties":{"tasks":{"type":"array","items":{"type":"object","properties":{"title":{"type":"string"},"priority":{"type":"string","enum":["low","medium","high"]},"completed":{"type":"boolean"}},"required":["title","priority","completed"],"additionalProperties":false}}},"required":["tasks"],"additionalProperties":false}`),
			},
		},
	})
}

func NewRegistry(definitions []Definition) *Registry {
	registry := &Registry{definitions: make(map[string]Definition, len(definitions))}
	for _, definition := range definitions {
		registry.definitions[definition.ID] = cloneDefinition(definition)
	}
	return registry
}

func (r *Registry) Get(id string) (*provider.ResponseFormat, bool) {
	if r == nil {
		return nil, false
	}
	definition, ok := r.definitions[id]
	if !ok {
		return nil, false
	}
	format := definition.Format
	format.Schema = append(json.RawMessage(nil), definition.Format.Schema...)
	return &format, true
}

func (r *Registry) Definitions() []Definition {
	items := make([]Definition, 0, len(r.definitions))
	for _, definition := range r.definitions {
		items = append(items, cloneDefinition(definition))
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items
}

func cloneDefinition(definition Definition) Definition {
	definition.Format.Schema = append(json.RawMessage(nil), definition.Format.Schema...)
	return definition
}
