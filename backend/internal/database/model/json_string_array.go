package model

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
)

// JSONStringArray maps a strongly typed string slice to PostgreSQL jsonb.
// It is used for server-owned IDs that must survive regeneration without
// accepting arbitrary client-provided JSON objects.
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
