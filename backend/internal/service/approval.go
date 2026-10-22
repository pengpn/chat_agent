package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"

	"github.com/example/agent-platform/backend/internal/domain"
)

// ApprovalKey 规范化不透明的目标工具参数，保留数字精度和全部字段。
// 这里处理 JSON 是为审批内容生成指纹，不承担工具解析或执行分派。
func ApprovalKey(target domain.ToolDescriptor, args string) (string, error) {
	var value any
	decoder := json.NewDecoder(strings.NewReader(args))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return "", ErrInvalidToolInput
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return "", ErrInvalidToolInput
	}
	if _, ok := value.(map[string]any); !ok {
		return "", ErrInvalidToolInput
	}
	raw, err := json.Marshal(struct {
		Tool domain.ToolDescriptor `json:"tool"`
		Args any                   `json:"args"`
	}{target, value})
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:]), nil
}
