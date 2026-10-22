package execution

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/google/uuid"
)

func (g *Gateway) readSnapshot(ctx context.Context, ws *workspace) (Snapshot, error) {
	snapshot := Snapshot{Files: make(map[string]File)}
	var total int64
	entries := 0
	err := fs.WalkDir(ws.root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if name == "." {
			return nil
		}
		entries++
		if entries > g.config.MaxEntries {
			return fmt.Errorf("工作区文件和目录数量超限")
		}
		if _, err := safePath(name, false); err != nil {
			return err
		}
		// 使用 Root.Lstat，不能依赖旧版 Root.FS 返回的 DirEntry.Info 的路径解析。
		info, err := ws.root.Lstat(name)
		if err != nil {
			return err
		}
		if info.IsDir() {
			snapshot.Directories = append(snapshot.Directories, name)
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("工作区禁止符号链接、硬链接导入和特殊文件: %s", name)
		}
		if info.Size() > g.config.MaxFileBytes || info.Size() > g.config.MaxWorkspaceBytes-total {
			return fmt.Errorf("工作区或单文件容量超限")
		}
		file, err := ws.root.Open(name)
		if err != nil {
			return err
		}
		data, err := io.ReadAll(io.LimitReader(file, g.config.MaxFileBytes+1))
		_ = file.Close()
		if err != nil {
			return err
		}
		if int64(len(data)) > g.config.MaxFileBytes || int64(len(data)) > g.config.MaxWorkspaceBytes-total {
			return fmt.Errorf("工作区或单文件容量超限")
		}
		total += int64(len(data))
		snapshot.Files[name] = File{Data: data, Mode: 0600 | info.Mode().Perm()&0111}
		return nil
	})
	return snapshot, err
}

// validateSnapshot 也用于校验 Runner 返回值，不能因为实现是测试替身就跳过边界。
func (g *Gateway) validateSnapshot(snapshot Snapshot) (Snapshot, error) {
	var total int64
	dirs := make(map[string]bool)
	for _, dir := range snapshot.Directories {
		if _, err := safePath(dir, false); err != nil {
			return Snapshot{}, err
		}
		dirs[dir] = true
	}
	for name, file := range snapshot.Files {
		if _, err := safePath(name, false); err != nil {
			return Snapshot{}, err
		}
		if dirs[name] {
			return Snapshot{}, fmt.Errorf("文件与目录重名: %s", name)
		}
		if int64(len(file.Data)) > g.config.MaxFileBytes || int64(len(file.Data)) > g.config.MaxWorkspaceBytes-total {
			return Snapshot{}, fmt.Errorf("工作区或单文件容量超限")
		}
		total += int64(len(file.Data))
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			dirs[parent] = true
		}
	}
	for dir := range dirs {
		if _, exists := snapshot.Files[dir]; exists {
			return Snapshot{}, fmt.Errorf("父路径不能是文件: %s", dir)
		}
		for parent := path.Dir(dir); parent != "."; parent = path.Dir(parent) {
			dirs[parent] = true
		}
	}
	if len(dirs)+len(snapshot.Files) > g.config.MaxEntries {
		return Snapshot{}, fmt.Errorf("工作区文件和目录数量超限")
	}
	snapshot.Directories = nil
	for dir := range dirs {
		snapshot.Directories = append(snapshot.Directories, dir)
	}
	sort.Strings(snapshot.Directories)
	return snapshot, nil
}

// replaceWorkspace 先写完整暂存目录，再切换目录；普通写入错误不会覆盖当前工作区。
// 暂存目录不挂载给容器，模型无法在宿主机上制造检查与写入之间的符号链接竞争。
func (g *Gateway) replaceWorkspace(ctx context.Context, ws *workspace, snapshot Snapshot) error {
	snapshot, err := g.validateSnapshot(snapshot)
	if err != nil {
		return err
	}
	stage := "stage-" + uuid.NewString()
	if err := g.root.Mkdir(stage, 0700); err != nil {
		return err
	}
	stagePath := filepath.Join(g.path, stage)
	defer os.RemoveAll(stagePath)
	root, err := g.root.OpenRoot(stage)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, dir := range snapshot.Directories {
		if err := mkdirParents(root, dir+"/placeholder"); err != nil {
			return err
		}
	}
	for name, item := range snapshot.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := mkdirParents(root, name); err != nil {
			return err
		}
		file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600|item.Mode.Perm()&0111)
		if err != nil {
			return err
		}
		_, writeErr := file.Write(item.Data)
		closeErr := file.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	backup := filepath.Join(g.path, "backup-"+uuid.NewString())
	if err := os.Rename(ws.path, backup); err != nil {
		return fmt.Errorf("工作区切换失败")
	}
	if err := os.Rename(stagePath, ws.path); err != nil {
		if restoreErr := os.Rename(backup, ws.path); restoreErr != nil {
			return fmt.Errorf("工作区恢复失败，需要管理员检查备份目录")
		}
		return fmt.Errorf("工作区切换失败，已恢复原文件")
	}
	_ = os.RemoveAll(backup)
	return nil
}

func pathsIn(snapshot Snapshot) []string {
	names := make([]string, 0, len(snapshot.Files))
	for name := range snapshot.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func isWithin(name, prefix string) bool {
	return prefix == "." || name == prefix || strings.HasPrefix(name, prefix+"/")
}
