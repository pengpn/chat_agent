package tool

import (
	"context"

	"github.com/example/agent-platform/backend/internal/domain"
)

type ArtifactStore interface {
	Save(ctx context.Context, input domain.SaveArtifactInput) (domain.SavedArtifact, error)
	Read(ctx context.Context, input domain.ReadArtifactInput) (domain.ArtifactPage, error)
}
type ArtifactSaveTool struct {
	toolSpec
	artifacts ArtifactStore
}

func NewArtifactSave(artifacts ArtifactStore) *ArtifactSaveTool {
	return &ArtifactSaveTool{newSpec("artifact", "save", "artifact", "持久化不可变文本产物，返回 artifactId。每次创建新版本，不写宿主文件。支持纯文本、Markdown、JSON，内容最多 32 KiB。", `{"type":"object","properties":{"title":{"type":"string","minLength":1,"maxLength":200},"mediaType":{"type":"string","enum":["text/plain","text/markdown","application/json"]},"content":{"type":"string","minLength":1}},"required":["title","mediaType","content"],"additionalProperties":false}`, "artifact"), artifacts}
}
func (t *ArtifactSaveTool) Execute(ctx context.Context, args string) (string, error) {
	return executeTyped(ctx, args, t.artifacts.Save)
}

type ArtifactReadTool struct {
	toolSpec
	artifacts ArtifactStore
}

func NewArtifactRead(artifacts ArtifactStore) *ArtifactReadTool {
	return &ArtifactReadTool{newSpec("artifact", "read", "artifact", "按 ID 分段读取当前会话产物；offset、limit 是字节数，下一页使用 nextOffset。", `{"type":"object","properties":{"artifactId":{"type":"string","format":"uuid"},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1,"maximum":4096}},"required":["artifactId"],"additionalProperties":false}`, "artifact"), artifacts}
}
func (t *ArtifactReadTool) Execute(ctx context.Context, args string) (string, error) {
	return executeTyped(ctx, args, t.artifacts.Read)
}
