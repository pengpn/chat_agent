// Package pagination provides opaque, stable cursors shared by list APIs.
package pagination

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"
)

var ErrInvalidCursor = errors.New("invalid pagination cursor")

// Cursor identifies the last row of a page. Time is the primary sort key and
// ID is the deterministic tie-breaker when multiple rows share a timestamp.
type Cursor struct {
	Time time.Time
	ID   string
}

type cursorPayload struct {
	Time string `json:"t"`
	ID   string `json:"i"`
}

func Encode(cursor Cursor) string {
	payload, _ := json.Marshal(cursorPayload{Time: cursor.Time.UTC().Format(time.RFC3339Nano), ID: cursor.ID})
	return base64.RawURLEncoding.EncodeToString(payload)
}

func Decode(raw string) (*Cursor, error) {
	if raw == "" {
		return nil, nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, ErrInvalidCursor
	}
	var decoded cursorPayload
	if err := json.Unmarshal(payload, &decoded); err != nil || !validUUID(decoded.ID) {
		return nil, ErrInvalidCursor
	}
	timestamp, err := time.Parse(time.RFC3339Nano, decoded.Time)
	if err != nil {
		return nil, ErrInvalidCursor
	}
	return &Cursor{Time: timestamp, ID: decoded.ID}, nil
}

func validUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, char := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if char != '-' {
				return false
			}
			continue
		}
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
			return false
		}
	}
	return true
}

// Page is the service-layer result returned by cursor-based list operations.
type Page[T any] struct {
	Items      []T
	NextCursor string
	HasMore    bool
}

func BuildPage[T any](items []T, limit int, cursorFor func(T) Cursor) Page[T] {
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	page := Page[T]{Items: items, HasMore: hasMore}
	if hasMore && len(items) > 0 {
		page.NextCursor = Encode(cursorFor(items[len(items)-1]))
	}
	return page
}
