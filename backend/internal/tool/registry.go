package tool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/example/agent-platform/backend/internal/domain"
	"github.com/example/agent-platform/backend/internal/provider"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Metadata 区分业务分类、来源与执行能力；分类本身不授予任何权限。
type Metadata = domain.ToolMetadata

// Descriptor 是固定的工具身份和 Schema 指纹，审批不能只绑定一个容易变化的名称。
type Descriptor = domain.ToolDescriptor
type ApprovalGuard interface {
	Authorize(context.Context, Descriptor, string) error
}
type registeredTool struct {
	Tool
	metadata   Metadata
	definition provider.ToolDefinition
	schemaHash string
	schema     *jsonschema.Schema
}

func (t *registeredTool) Descriptor() Descriptor {
	return Descriptor{ToolMetadata: t.metadata, SchemaHash: t.schemaHash}
}
func (t *registeredTool) validate(raw string) error {
	value, err := jsonschema.UnmarshalJSON(strings.NewReader(raw))
	if err != nil {
		return fmt.Errorf("工具参数不是有效 JSON")
	}
	if err := t.schema.Validate(value); err != nil {
		return fmt.Errorf("工具参数不符合服务端 JSON Schema")
	}
	return nil
}

var componentName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
var versionName = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

// Register 返回重复注册/别名冲突错误，不再静默覆盖。新版本注册后需显式 Activate。
func (r *Registry) Register(item Tool) error {
	if item == nil {
		return fmt.Errorf("不能注册空工具")
	}
	definition := item.Definition()
	metadata := Metadata{Namespace: "legacy", Name: definition.Function.Name, Version: "1.0.0", Category: "other", Source: "builtin", Exposure: "model", WireName: definition.Function.Name}
	if described, ok := item.(interface{ Metadata() Metadata }); ok {
		metadata = described.Metadata()
	}
	if !componentName.MatchString(metadata.Namespace) || !componentName.MatchString(metadata.Name) || !componentName.MatchString(metadata.Source) || !versionName.MatchString(metadata.Version) || !componentName.MatchString(metadata.WireName) || metadata.WireName != definition.Function.Name || definition.Type != "function" {
		return fmt.Errorf("工具元数据无效: %s", metadata.ID())
	}
	document, err := jsonschema.UnmarshalJSON(strings.NewReader(string(definition.Function.Parameters)))
	if err != nil {
		return err
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("urn:tool-schema", document); err != nil {
		return err
	}
	schema, err := compiler.Compile("urn:tool-schema")
	if err != nil {
		return err
	}
	// 复制切片，防止调用者修改已固定的定义。
	definition.Function.Parameters = append(json.RawMessage(nil), definition.Function.Parameters...)
	metadata.Capabilities = append([]string(nil), metadata.Capabilities...)
	canonical, err := json.Marshal(document)
	if err != nil {
		return fmt.Errorf("工具 Schema 无法生成指纹: %w", err)
	}
	hash := sha256.Sum256(canonical)
	entry := &registeredTool{item, metadata, definition, hex.EncodeToString(hash[:]), schema}
	r.mu.Lock()
	defer r.mu.Unlock()
	key := metadata.ID() + "@" + metadata.Version
	if _, ok := r.versions[key]; ok {
		return fmt.Errorf("工具版本重复注册: %s", key)
	}
	for _, existing := range r.versions {
		if existing.metadata.ID() == metadata.ID() && existing.metadata.WireName != metadata.WireName {
			return fmt.Errorf("同一逻辑工具的版本必须保持相同 WireName")
		}
		if existing.metadata.WireName == metadata.WireName && existing.metadata.ID() != metadata.ID() {
			return fmt.Errorf("工具供应商名称冲突: %s", metadata.WireName)
		}
	}
	r.versions[key] = entry
	if _, ok := r.tools[metadata.WireName]; !ok {
		r.tools[metadata.WireName] = entry
	}
	return nil
}
func (r *Registry) Activate(id, version string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.versions[id+"@"+version]
	if !ok {
		return fmt.Errorf("工具版本不存在")
	}
	r.tools[entry.metadata.WireName] = entry
	return nil
}
func (r *Registry) SetApprovalGuard(guard ApprovalGuard) { r.guard = guard }

// ToolSet 固定本次业务消息看见的定义和执行器。后续注册、切版本不会影响本轮。
type ToolSet struct {
	registry *Registry
	userID   string
	tools    map[string]*registeredTool
}
type toolSetKey struct{}

func (r *Registry) SnapshotForUser(ctx context.Context, userID string) *ToolSet {
	r.mu.RLock()
	items := make(map[string]*registeredTool, len(r.tools))
	for name, item := range r.tools {
		items[name] = item
	}
	r.mu.RUnlock()
	for name := range items {
		allowed, err := r.policy.Allowed(ctx, userID, name)
		if err != nil || !allowed {
			delete(items, name)
		}
	}
	return &ToolSet{registry: r, userID: userID, tools: items}
}
func (s *ToolSet) Definitions() []provider.ToolDefinition {
	definitions := make([]provider.ToolDefinition, 0, len(s.tools))
	for _, item := range s.tools {
		d := item.definition
		d.Function.Parameters = append(json.RawMessage(nil), d.Function.Parameters...)
		definitions = append(definitions, d)
	}
	sort.Slice(definitions, func(i, j int) bool { return definitions[i].Function.Name < definitions[j].Function.Name })
	return definitions
}
func (s *ToolSet) Execute(ctx context.Context, userID string, call provider.ToolCall) (string, error) {
	if userID != s.userID {
		return "", fmt.Errorf("工具快照用户身份不一致")
	}
	item, ok := s.tools[call.Function.Name]
	if !ok {
		return "", fmt.Errorf("tool %q is not registered or not allowed for this user/turn", call.Function.Name)
	}
	return s.registry.execute(context.WithValue(ctx, toolSetKey{}, s), userID, item, call)
}

// ResolveApproval 只允许本轮已向模型暴露、确实需要审批的工具。
func ResolveApproval(ctx context.Context, name, arguments string) (Descriptor, error) {
	set, ok := ctx.Value(toolSetKey{}).(*ToolSet)
	if !ok {
		return Descriptor{}, fmt.Errorf("缺少本轮工具快照")
	}
	item, ok := set.tools[name]
	if !ok || !item.metadata.RequiresApproval {
		return Descriptor{}, fmt.Errorf("目标工具不可见或无需审批")
	}
	if err := item.validate(arguments); err != nil {
		return Descriptor{}, err
	}
	return item.Descriptor(), nil
}
