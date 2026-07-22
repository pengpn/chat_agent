package streamcache

import (
	"testing"
	"time"
)

func TestCacheReplaysEventsAfterSequence(t *testing.T) {
	cache := New(time.Minute, 10)
	cache.Start("message-1", "user-1")
	first, _ := cache.Append("message-1", "delta", map[string]any{"content": "A"}, false)
	second, _ := cache.Append("message-1", "done", map[string]any{"status": "complete"}, true)
	if first.Sequence != 1 || second.Sequence != 2 {
		t.Fatalf("sequences = %d, %d", first.Sequence, second.Sequence)
	}
	snapshot := cache.Snapshot("message-1", "user-1", 1)
	if !snapshot.Found || !snapshot.Terminal || snapshot.Gap || len(snapshot.Events) != 1 || snapshot.Events[0].Sequence != 2 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if cache.Snapshot("message-1", "other-user", 0).Found {
		t.Fatal("stream was visible to another user")
	}
}

func TestCacheReportsSequenceGap(t *testing.T) {
	cache := New(time.Minute, 2)
	cache.Start("message-1", "user-1")
	cache.Append("message-1", "delta", map[string]any{"content": "A"}, false)
	cache.Append("message-1", "delta", map[string]any{"content": "B"}, false)
	cache.Append("message-1", "delta", map[string]any{"content": "C"}, false)
	if snapshot := cache.Snapshot("message-1", "user-1", 0); !snapshot.Gap {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}
