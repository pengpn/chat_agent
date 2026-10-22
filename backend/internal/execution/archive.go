package execution

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"github.com/example/agent-platform/backend/internal/config"
)

// packSnapshot 不传入宿主路径，只传入已校验的普通文件；容器接触不到工作区父目录。
func packSnapshot(snapshot Snapshot) ([]byte, error) {
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	if err := writer.WriteHeader(&tar.Header{Name: ".", Typeflag: tar.TypeDir, Mode: 0700, Uid: 65532, Gid: 65532}); err != nil {
		return nil, err
	}
	for _, name := range snapshot.Directories {
		if err := writer.WriteHeader(&tar.Header{Name: name + "/", Typeflag: tar.TypeDir, Mode: 0700, Uid: 65532, Gid: 65532}); err != nil {
			return nil, err
		}
	}
	for _, name := range pathsIn(snapshot) {
		item := snapshot.Files[name]
		if err := writer.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Size: int64(len(item.Data)), Mode: int64(0600 | item.Mode.Perm()&0111), Uid: 65532, Gid: 65532}); err != nil {
			return nil, err
		}
		if _, err := writer.Write(item.Data); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// unpackSnapshot 不使用 docker cp 的宿主目录解包模式。所有路径与文件类型均由 Go 校验，
// 符号链接、硬链接、设备、重复项及超大归档直接拒绝，不能落到宿主文件系统。
func unpackSnapshot(reader io.Reader, cfg config.ExecutionConfig) (Snapshot, error) {
	snapshot := Snapshot{Files: make(map[string]File)}
	seen := make(map[string]bool)
	archive := tar.NewReader(reader)
	var total int64
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return Snapshot{}, fmt.Errorf("工作区归档损坏")
		}
		name := strings.TrimSuffix(strings.TrimPrefix(header.Name, "./"), "/")
		if name == "." || name == "" {
			if header.Typeflag != tar.TypeDir {
				return Snapshot{}, fmt.Errorf("归档根节点不是目录")
			}
			continue
		}
		if _, err := safePath(name, false); err != nil {
			return Snapshot{}, err
		}
		if seen[name] || len(seen) >= cfg.MaxEntries {
			return Snapshot{}, fmt.Errorf("归档包含重复路径或目录项过多")
		}
		seen[name] = true
		switch header.Typeflag {
		case tar.TypeDir:
			snapshot.Directories = append(snapshot.Directories, name)
		case tar.TypeReg, tar.TypeRegA:
			if header.Size < 0 || header.Size > cfg.MaxFileBytes || header.Size > cfg.MaxWorkspaceBytes-total {
				return Snapshot{}, fmt.Errorf("Shell 输出的工作区超过容量限制")
			}
			data, err := io.ReadAll(io.LimitReader(archive, header.Size+1))
			if err != nil || int64(len(data)) != header.Size {
				return Snapshot{}, fmt.Errorf("工作区文件数据不完整")
			}
			total += header.Size
			snapshot.Files[name] = File{Data: data, Mode: 0600 | fs.FileMode(header.Mode)&0111}
		default:
			return Snapshot{}, fmt.Errorf("工作区禁止符号链接、硬链接和特殊文件: %s", name)
		}
	}
	return snapshot, nil
}
