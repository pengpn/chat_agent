package logger

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
)

func TestInfoIncludesRequestIDAndFields(t *testing.T) {
	var output bytes.Buffer
	configured, err := newLogger("info", &output)
	if err != nil {
		t.Fatal(err)
	}
	previous := defaultZap()
	setDefault(configured)
	t.Cleanup(func() { setDefault(previous) })

	ctx := WithRequestID(context.Background(), "req-123")
	Info(ctx, "message persisted", String("message_id", "msg-1"), Int64("total_tokens", 42))

	var entry map[string]any
	if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
		t.Fatalf("decode log = %v; raw = %s", err, output.String())
	}
	if entry["request_id"] != "req-123" || entry["message_id"] != "msg-1" || entry["total_tokens"] != float64(42) {
		t.Fatalf("entry = %#v", entry)
	}
}
