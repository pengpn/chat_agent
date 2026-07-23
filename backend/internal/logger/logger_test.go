package logger

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestDailyFileWriterRotatesByLocalDate(t *testing.T) {
	now := time.Date(2026, time.July, 23, 23, 59, 0, 0, time.Local)
	writer, err := newDailyFileWriter(t.TempDir(), 30, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("first\n")); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if _, err := writer.Write([]byte("second\n")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	first, err := os.ReadFile(filepath.Join(writer.directory, "agent-platform-2026-07-23.log"))
	if err != nil || string(first) != "first\n" {
		t.Fatalf("first date log=%q err=%v", first, err)
	}
	second, err := os.ReadFile(filepath.Join(writer.directory, "agent-platform-2026-07-24.log"))
	if err != nil || string(second) != "second\n" {
		t.Fatalf("second date log=%q err=%v", second, err)
	}
}
