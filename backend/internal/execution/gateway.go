// Package execution 提供会话隔离的文件操作和 Docker 执行边界，不允许模型直接操作宿主机。
package execution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/example/agent-platform/backend/internal/config"
)

type Scope struct{ UserID, ConversationID, OperationID string }
type scopeKey struct{}

// WithScope 的身份由鉴权后的业务服务写入，不能从模型参数反序列化。
func WithScope(ctx context.Context, scope Scope) context.Context {
	return context.WithValue(ctx, scopeKey{}, scope)
}
func ScopeFromContext(ctx context.Context) (Scope, error) {
	scope, ok := ctx.Value(scopeKey{}).(Scope)
	if !ok || scope.UserID == "" || scope.ConversationID == "" || len(scope.UserID) > 256 || len(scope.ConversationID) > 256 || strings.ContainsRune(scope.UserID+scope.ConversationID, 0) {
		return Scope{}, fmt.Errorf("执行工具缺少服务端用户或会话身份")
	}
	return scope, nil
}

type File struct {
	Data []byte
	Mode fs.FileMode
}
type Snapshot struct {
	Files       map[string]File
	Directories []string
}
type ShellResult struct {
	Output          string `json:"output"`
	ExitCode        int    `json:"exitCode"`
	Truncated       bool   `json:"truncated"`
	WorkspaceSynced bool   `json:"workspaceSynced"`
}

// Runner 只接收受限快照，不接收宿主目录；实现必须在进程退出后返回稳定的快照。
type Runner interface {
	Run(context.Context, Snapshot, string) (ShellResult, Snapshot, error)
}

type Gateway struct {
	config    config.ExecutionConfig
	root      *os.Root
	path      string
	runner    Runner
	slots     chan struct{}
	locks     [64]chan struct{}
	closeOnce sync.Once
}

// New 关闭时不创建目录或连接 Docker；测试可以注入不调用真实 Docker 的 Runner。
func New(cfg config.ExecutionConfig, runner Runner) (*Gateway, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	if cfg.MaxConcurrent <= 0 || cfg.MaxWorkspaceBytes <= 0 || cfg.MaxFileBytes <= 0 || cfg.MaxEntries <= 0 || cfg.MaxOutputBytes <= 0 {
		return nil, fmt.Errorf("执行工具缺少有效资源限制")
	}
	base, err := filepath.Abs(cfg.WorkspaceRoot)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(base, 0700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return nil, err
	}
	if runner == nil {
		runner, err = NewDockerRunner(cfg)
		if err != nil {
			_ = root.Close()
			return nil, err
		}
	}
	g := &Gateway{config: cfg, root: root, path: base, runner: runner, slots: make(chan struct{}, cfg.MaxConcurrent)}
	for i := range g.locks {
		g.locks[i] = make(chan struct{}, 1)
	}
	return g, nil
}

func (g *Gateway) Close() {
	if g != nil {
		g.closeOnce.Do(func() { _ = g.root.Close() })
	}
}

type workspace struct {
	root      *os.Root
	path, key string
}

// withWorkspace 同一会话串行执行，文件工具与 Shell 不会互相覆盖正在修改的数据。
// 固定数量的锁避免为每个历史会话永久分配一把锁。
func (g *Gateway) withWorkspace(ctx context.Context, fn func(*workspace) error) error {
	if g == nil {
		return fmt.Errorf("执行工具未开启")
	}
	scope, err := ScopeFromContext(ctx)
	if err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(scope.UserID + "\x00" + scope.ConversationID))
	key := hex.EncodeToString(digest[:])
	lock := g.locks[int(digest[0])%len(g.locks)]
	select {
	case lock <- struct{}{}:
		defer func() { <-lock }()
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case g.slots <- struct{}{}:
		defer func() { <-g.slots }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := g.root.Mkdir(key, 0700); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	info, err := g.root.Lstat(key)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("工作区必须是普通目录")
	}
	root, err := g.root.OpenRoot(key)
	if err != nil {
		return err
	}
	defer root.Close()
	return fn(&workspace{root: root, path: filepath.Join(g.path, key), key: key})
}

func safePath(name string, allowRoot bool) (string, error) {
	if name == "" && allowRoot {
		return ".", nil
	}
	if name == "." && allowRoot {
		return name, nil
	}
	if name == "." {
		return "", fmt.Errorf("文件路径不能是工作区根目录")
	}
	if len(name) > 1024 || strings.ContainsAny(name, "\\\x00") || !fs.ValidPath(name) || path.IsAbs(name) {
		return "", fmt.Errorf("路径必须是工作区内的相对路径，禁止绝对路径、反斜杠和 ..")
	}
	for _, part := range strings.Split(name, "/") {
		if len(part) > 240 {
			return "", fmt.Errorf("路径分量过长")
		}
	}
	return name, nil
}

func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func mkdirParents(root *os.Root, name string) error {
	parent := path.Dir(name)
	if parent == "." {
		return nil
	}
	current := ""
	for _, part := range strings.Split(parent, "/") {
		current = path.Join(current, part)
		if err := root.Mkdir(current, 0700); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		info, err := root.Lstat(current)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("父路径不是普通目录")
		}
	}
	return nil
}
