package userevent

import (
	"encoding/json"
	"testing"
	"time"
)

func TestHubPublishesPerUserAndReplaysAfterSequence(t *testing.T) {
	hub := New(time.Minute, 10)
	hub.Publish("user-1", "conversation.title.updated", map[string]any{"title": "标题一"})
	hub.Publish("user-1", "conversation.title.updated", map[string]any{"title": "标题二"})

	snapshot := hub.Snapshot("user-1", 1)
	if snapshot.Gap || len(snapshot.Events) != 1 || snapshot.Events[0].Sequence != 2 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	var data map[string]any
	if err := json.Unmarshal(snapshot.Events[0].Data, &data); err != nil {
		t.Fatal(err)
	}
	if data["title"] != "标题二" || data["sequence"] != float64(2) {
		t.Fatalf("data = %#v", data)
	}
	if other := hub.Snapshot("user-2", 0); len(other.Events) != 0 {
		t.Fatalf("other user events = %#v", other.Events)
	}
}

func TestHubReportsTruncatedAndRestartedSequenceGaps(t *testing.T) {
	hub := New(time.Minute, 1)
	hub.Publish("user-1", "one", nil)
	hub.Publish("user-1", "two", nil)
	if snapshot := hub.Snapshot("user-1", 0); !snapshot.Gap {
		t.Fatalf("truncated snapshot = %#v", snapshot)
	}
	if snapshot := New(time.Minute, 1).Snapshot("user-1", 2); !snapshot.Gap {
		t.Fatalf("restarted snapshot = %#v", snapshot)
	}
}
