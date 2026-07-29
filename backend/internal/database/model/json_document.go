package model

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
)

// JSONDocument 在 API 中保持 JSON 对象/数组类型，并通过 PostgreSQL jsonb
// 持久化，避免将结构化结果退化成需要客户端再次解析的字符串。
type JSONDocument json.RawMessage

func (document JSONDocument) Value() (driver.Value, error) {
	if len(document) == 0 {
		return nil, nil
	}
	if !json.Valid(document) {
		return nil, errors.New("invalid JSON document")
	}
	return string(document), nil
}

func (document *JSONDocument) Scan(value any) error {
	if value == nil {
		*document = nil
		return nil
	}
	var raw []byte
	switch typed := value.(type) {
	case []byte:
		raw = typed
	case string:
		raw = []byte(typed)
	default:
		return fmt.Errorf("scan JSON document from %T", value)
	}
	if !json.Valid(raw) {
		return errors.New("database returned invalid JSON document")
	}
	*document = append((*document)[:0], raw...)
	return nil
}

func (document JSONDocument) MarshalJSON() ([]byte, error) {
	if len(document) == 0 {
		return []byte("null"), nil
	}
	return document, nil
}
