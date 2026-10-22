package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

type Entry struct {
	Path  string `json:"path"`
	Type  string `json:"type"`
	Bytes int    `json:"bytes"`
}
type ListResult struct {
	Entries    []Entry `json:"entries"`
	NextOffset int     `json:"nextOffset"`
	HasMore    bool    `json:"hasMore"`
}
type ReadResult struct {
	Path       string `json:"path"`
	Content    string `json:"content"`
	SHA256     string `json:"sha256"`
	Bytes      int    `json:"bytes"`
	NextOffset int    `json:"nextOffset"`
	EOF        bool   `json:"eof"`
}
type WriteResult struct {
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
	Bytes   int    `json:"bytes"`
	Created bool   `json:"created"`
}
type Edit struct {
	OldText string `json:"oldText"`
	NewText string `json:"newText"`
}
type SearchMatch struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
}
type SearchResult struct {
	Matches   []SearchMatch `json:"matches"`
	Truncated bool          `json:"truncated"`
}

func (g *Gateway) List(ctx context.Context, name string, offset, limit int) (result ListResult, err error) {
	result.Entries = make([]Entry, 0)
	name, err = safePath(name, true)
	if err != nil {
		return
	}
	if offset < 0 || limit < 0 || limit > 100 {
		return result, fmt.Errorf("目录分页参数无效")
	}
	if limit == 0 {
		limit = 50
	}
	err = g.withWorkspace(ctx, func(ws *workspace) error {
		snapshot, err := g.readSnapshot(ctx, ws)
		if err != nil {
			return err
		}
		if name != "." {
			found := false
			for _, dir := range snapshot.Directories {
				if dir == name {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("目录不存在")
			}
		}
		var entries []Entry
		for file, item := range snapshot.Files {
			if path.Dir(file) == name {
				entries = append(entries, Entry{Path: file, Type: "file", Bytes: len(item.Data)})
			}
		}
		for _, dir := range snapshot.Directories {
			if path.Dir(dir) == name {
				entries = append(entries, Entry{Path: dir, Type: "directory"})
			}
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
		if offset > len(entries) {
			return fmt.Errorf("目录偏移超出范围")
		}
		used := 0
		for i := offset; i < len(entries) && len(result.Entries) < limit; i++ {
			encoded, _ := json.Marshal(entries[i])
			if used+len(encoded) > g.config.MaxOutputBytes {
				break
			}
			used += len(encoded)
			result.Entries = append(result.Entries, entries[i])
		}
		result.NextOffset = offset + len(result.Entries)
		result.HasMore = result.NextOffset < len(entries)
		if result.HasMore && len(result.Entries) == 0 {
			return fmt.Errorf("输出预算不足以返回一个目录项")
		}
		return nil
	})
	return
}

func (g *Gateway) Read(ctx context.Context, name string, offset, limit int) (result ReadResult, err error) {
	name, err = safePath(name, false)
	if err != nil {
		return
	}
	if offset < 0 || limit < 0 || limit > g.config.MaxOutputBytes {
		return result, fmt.Errorf("读取偏移或字节上限无效")
	}
	if limit == 0 {
		limit = g.config.MaxOutputBytes
	}
	err = g.withWorkspace(ctx, func(ws *workspace) error {
		snapshot, err := g.readSnapshot(ctx, ws)
		if err != nil {
			return err
		}
		item, ok := snapshot.Files[name]
		if !ok {
			return fmt.Errorf("文件不存在")
		}
		if !utf8.Valid(item.Data) || bytes.IndexByte(item.Data, 0) >= 0 {
			return fmt.Errorf("file_read 只支持不含 NUL 的 UTF-8 文本")
		}
		if offset > len(item.Data) || (offset < len(item.Data) && !utf8.RuneStart(item.Data[offset])) {
			return fmt.Errorf("读取偏移不在有效字符边界")
		}
		end := min(offset+limit, len(item.Data))
		for end > offset && end < len(item.Data) && !utf8.RuneStart(item.Data[end]) {
			end--
		}
		if end == offset && offset < len(item.Data) {
			return fmt.Errorf("读取上限不足以包含一个完整字符")
		}
		result = ReadResult{Path: name, Content: string(item.Data[offset:end]), SHA256: digest(item.Data), Bytes: len(item.Data), NextOffset: end, EOF: end == len(item.Data)}
		return nil
	})
	return
}

func (g *Gateway) Write(ctx context.Context, name, content, expected string) (result WriteResult, err error) {
	return g.update(ctx, name, expected, true, func(_ File) ([]byte, error) { return []byte(content), nil })
}

func (g *Gateway) ApplyPatch(ctx context.Context, name, expected string, edits []Edit) (WriteResult, error) {
	if len(edits) == 0 || len(edits) > 20 || expected == "" {
		return WriteResult{}, fmt.Errorf("增量修改必须提供文件 SHA-256 和 1～20 个替换项")
	}
	return g.update(ctx, name, expected, false, func(file File) ([]byte, error) {
		text := string(file.Data)
		for _, edit := range edits {
			if edit.OldText == "" || strings.Count(text, edit.OldText) != 1 {
				return nil, fmt.Errorf("oldText 必须非空且在当前文本中恰好匹配一次")
			}
			if !utf8.ValidString(edit.NewText) || strings.ContainsRune(edit.NewText, 0) {
				return nil, fmt.Errorf("替换文本必须是有效 UTF-8 文本")
			}
			text = strings.Replace(text, edit.OldText, edit.NewText, 1)
			if int64(len(text)) > g.config.MaxFileBytes {
				return nil, fmt.Errorf("修改后文件过大")
			}
		}
		return []byte(text), nil
	})
}

func (g *Gateway) update(ctx context.Context, name, expected string, allowCreate bool, change func(File) ([]byte, error)) (result WriteResult, err error) {
	name, err = safePath(name, false)
	if err != nil {
		return
	}
	err = g.withWorkspace(ctx, func(ws *workspace) error {
		snapshot, err := g.readSnapshot(ctx, ws)
		if err != nil {
			return err
		}
		previous, exists := snapshot.Files[name]
		if exists && (expected == "" || expected != digest(previous.Data)) {
			return fmt.Errorf("文件已存在或版本变化，请先 file_read 再携带最新 expectedSha256 修改")
		}
		if !exists && (!allowCreate || expected != "") {
			return fmt.Errorf("文件不存在或创建条件不匹配")
		}
		data, err := change(previous)
		if err != nil {
			return err
		}
		if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
			return fmt.Errorf("文件工具只写入不含 NUL 的 UTF-8 文本")
		}
		snapshot.Files[name] = File{Data: data, Mode: 0600 | previous.Mode.Perm()&0111}
		if err := g.replaceWorkspace(ctx, ws, snapshot); err != nil {
			return err
		}
		result = WriteResult{Path: name, SHA256: digest(data), Bytes: len(data), Created: !exists}
		return nil
	})
	return
}

func (g *Gateway) Search(ctx context.Context, prefix, pattern string, regex bool, limit int) (result SearchResult, err error) {
	result.Matches = make([]SearchMatch, 0)
	prefix, err = safePath(prefix, true)
	if err != nil {
		return
	}
	if pattern == "" || len(pattern) > 256 || limit < 0 || limit > 100 {
		return result, fmt.Errorf("搜索表达式或结果上限无效")
	}
	if limit == 0 {
		limit = 20
	}
	if !regex {
		pattern = regexp.QuoteMeta(pattern)
	}
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return result, fmt.Errorf("正则表达式无效")
	}
	err = g.withWorkspace(ctx, func(ws *workspace) error {
		snapshot, err := g.readSnapshot(ctx, ws)
		if err != nil {
			return err
		}
		used := 0
		for _, name := range pathsIn(snapshot) {
			if !isWithin(name, prefix) {
				continue
			}
			data := snapshot.Files[name].Data
			if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
				continue
			}
			for number, line := range strings.Split(string(data), "\n") {
				if err := ctx.Err(); err != nil {
					return err
				}
				location := compiled.FindStringIndex(line)
				if location == nil {
					continue
				}
				// 只返回命中附近的短片段，避免超长行耗尽上下文。
				start, end := max(0, location[0]-80), min(len(line), location[1]+160)
				end = min(end, start+320)
				for start > 0 && !utf8.RuneStart(line[start]) {
					start--
				}
				for end < len(line) && end > start && !utf8.RuneStart(line[end]) {
					end--
				}
				match := SearchMatch{Path: name, Line: number + 1, Text: line[start:end], Truncated: start > 0 || end < len(line)}
				encoded, _ := json.Marshal(match)
				if len(result.Matches) == limit || used+len(encoded) > g.config.MaxOutputBytes {
					result.Truncated = true
					return nil
				}
				used += len(encoded)
				result.Matches = append(result.Matches, match)
			}
		}
		return nil
	})
	return
}

func (g *Gateway) Shell(ctx context.Context, command string) (result ShellResult, err error) {
	if strings.TrimSpace(command) == "" || len(command) > 16<<10 || strings.ContainsRune(command, 0) {
		return result, fmt.Errorf("Shell 命令必须为 1～16384 字节的文本")
	}
	ctx, cancel := context.WithTimeout(ctx, g.config.Timeout)
	defer cancel()
	err = g.withWorkspace(ctx, func(ws *workspace) error {
		snapshot, err := g.readSnapshot(ctx, ws)
		if err != nil {
			return err
		}
		var updated Snapshot
		result, updated, err = g.runner.Run(ctx, snapshot, command)
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := g.replaceWorkspace(ctx, ws, updated); err != nil {
			return err
		}
		result.WorkspaceSynced = true
		return nil
	})
	return
}
