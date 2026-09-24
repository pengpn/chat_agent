package model

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
)

// JSONStringArray 把强类型字符串切片映射为 PostgreSQL jsonb。
// 用来保存服务端自己签发的 ID：重新生成回答时这些 ID 必须原样保留，
// 同时拒绝客户端塞进来的任意 JSON 对象。
type JSONStringArray []string

func (values JSONStringArray) Value() (driver.Value, error) {
	if len(values) == 0 {
		return nil, nil
	}
	encoded, err := json.Marshal([]string(values))
	if err != nil {
		return nil, err
	}
	return string(encoded), nil
}

func (values *JSONStringArray) Scan(value any) error {
	if value == nil {
		*values = nil
		return nil
	}
	var raw []byte
	switch typed := value.(type) {
	case []byte:
		raw = typed
	case string:
		raw = []byte(typed)
	default:
		return fmt.Errorf("scan JSON string array from %T", value)
	}
	var decoded []string
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	for _, item := range decoded {
		if item == "" {
			return errors.New("JSON string array contains an empty value")
		}
	}
	*values = append((*values)[:0], decoded...)
	return nil
}
