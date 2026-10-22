package tool

import (
	"context"

	"github.com/example/agent-platform/backend/internal/execution"
)

// FileOperations 由执行网关实现；宿主路径限制、版本检查等仍属于网关职责。
type FileOperations interface {
	List(ctx context.Context, path string, offset, limit int) (execution.ListResult, error)
	Read(ctx context.Context, path string, offset, limit int) (execution.ReadResult, error)
	Write(ctx context.Context, path, content, expectedSHA256 string) (execution.WriteResult, error)
	ApplyPatch(ctx context.Context, path, expectedSHA256 string, edits []execution.Edit) (execution.WriteResult, error)
	Search(ctx context.Context, path, pattern string, regex bool, limit int) (execution.SearchResult, error)
}
type filePageInput struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}
type fileWriteInput struct {
	Path     string `json:"path"`
	Content  string `json:"content"`
	Expected string `json:"expectedSha256"`
}
type filePatchInput struct {
	Path     string           `json:"path"`
	Expected string           `json:"expectedSha256"`
	Edits    []execution.Edit `json:"edits"`
}
type fileSearchInput struct {
	Path    string `json:"path"`
	Pattern string `json:"pattern"`
	Regex   bool   `json:"regex"`
	Limit   int    `json:"limit"`
}

type FileListTool struct {
	toolSpec
	files FileOperations
}

func NewFileList(files FileOperations) *FileListTool {
	return &FileListTool{newSpec("file", "list", "file", "分页列出当前会话工作区目录，path 为相对路径，省略为根目录；offset、limit 为目录项数量。", `{"type":"object","properties":{"path":{"type":"string"},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1,"maximum":100}},"additionalProperties":false}`, "read"), files}
}
func (t *FileListTool) Execute(ctx context.Context, args string) (string, error) {
	return executeTyped(ctx, args, func(ctx context.Context, in filePageInput) (execution.ListResult, error) {
		return t.files.List(ctx, in.Path, in.Offset, in.Limit)
	})
}

type FileReadTool struct {
	toolSpec
	files FileOperations
}

func NewFileRead(files FileOperations) *FileReadTool {
	return &FileReadTool{newSpec("file", "read", "file", "分段读取工作区 UTF-8 文本，返回完整文件 SHA-256。offset、limit 以字节计，继续读取使用 nextOffset；修改前先读取并获取 sha256。", `{"type":"object","properties":{"path":{"type":"string","minLength":1},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1}},"required":["path"],"additionalProperties":false}`, "read"), files}
}
func (t *FileReadTool) Execute(ctx context.Context, args string) (string, error) {
	return executeTyped(ctx, args, func(ctx context.Context, in filePageInput) (execution.ReadResult, error) {
		return t.files.Read(ctx, in.Path, in.Offset, in.Limit)
	})
}

type FileWriteTool struct {
	toolSpec
	files FileOperations
}

func NewFileWrite(files FileOperations) *FileWriteTool {
	return &FileWriteTool{newSpec("file", "write", "file", "创建或覆盖当前会话工作区的 UTF-8 文本，自动创建父目录。覆盖必须传 file_read 返回的 expectedSha256，版本不匹配拒绝，不允许盲目覆盖。", `{"type":"object","properties":{"path":{"type":"string","minLength":1},"content":{"type":"string"},"expectedSha256":{"type":"string"}},"required":["path","content"],"additionalProperties":false}`, "write"), files}
}
func (t *FileWriteTool) Execute(ctx context.Context, args string) (string, error) {
	return executeTyped(ctx, args, func(ctx context.Context, in fileWriteInput) (execution.WriteResult, error) {
		return t.files.Write(ctx, in.Path, in.Content, in.Expected)
	})
}

type FileApplyPatchTool struct {
	toolSpec
	files FileOperations
}

func NewFileApplyPatch(files FileOperations) *FileApplyPatchTool {
	return &FileApplyPatchTool{newSpec("file", "apply_patch", "file", "顺序精确替换一个现有文件的文本。必须携带最新 expectedSha256，每个 oldText 非空且恰好匹配一次。不是 unified diff；任一项失败都不写文件。", `{"type":"object","properties":{"path":{"type":"string","minLength":1},"expectedSha256":{"type":"string","minLength":64,"maxLength":64},"edits":{"type":"array","minItems":1,"maxItems":20,"items":{"type":"object","properties":{"oldText":{"type":"string","minLength":1},"newText":{"type":"string"}},"required":["oldText","newText"],"additionalProperties":false}}},"required":["path","expectedSha256","edits"],"additionalProperties":false}`, "write"), files}
}
func (t *FileApplyPatchTool) Execute(ctx context.Context, args string) (string, error) {
	return executeTyped(ctx, args, func(ctx context.Context, in filePatchInput) (execution.WriteResult, error) {
		return t.files.ApplyPatch(ctx, in.Path, in.Expected, in.Edits)
	})
}

type FileSearchTool struct {
	toolSpec
	files FileOperations
}

func NewFileSearch(files FileOperations) *FileSearchTool {
	return &FileSearchTool{newSpec("file", "search", "file", "递归搜索当前会话工作区文本，返回相对路径、行号和短片段。默认字面量匹配；regex=true 使用 Go RE2 正则。truncated=true 时应缩小路径或模式范围。", `{"type":"object","properties":{"path":{"type":"string"},"pattern":{"type":"string","minLength":1,"maxLength":256},"regex":{"type":"boolean"},"limit":{"type":"integer","minimum":1,"maximum":100}},"required":["pattern"],"additionalProperties":false}`, "read"), files}
}
func (t *FileSearchTool) Execute(ctx context.Context, args string) (string, error) {
	return executeTyped(ctx, args, func(ctx context.Context, in fileSearchInput) (execution.SearchResult, error) {
		return t.files.Search(ctx, in.Path, in.Pattern, in.Regex, in.Limit)
	})
}
