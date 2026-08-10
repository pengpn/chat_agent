package pagination

import (
	"errors"
	"testing"
	"time"
)

func TestCursorRoundTrip(t *testing.T) {
	want := Cursor{Time: time.Date(2026, 8, 10, 10, 11, 12, 345, time.FixedZone("CST", 8*60*60)), ID: "018f1234-5678-7abc-8def-0123456789ab"}
	got, err := Decode(Encode(want))
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != want.ID || !got.Time.Equal(want.Time) {
		t.Fatalf("cursor = %#v, want %#v", got, want)
	}
}

func TestCursorRejectsMalformedInput(t *testing.T) {
	badID := Encode(Cursor{Time: time.Now(), ID: "not-a-uuid"})
	for _, raw := range []string{"%%%", "e30", "eyJ0Ijoibm90LWEtdGltZSIsImkiOiIxIn0", badID} {
		if _, err := Decode(raw); !errors.Is(err, ErrInvalidCursor) {
			t.Fatalf("Decode(%q) error = %v", raw, err)
		}
	}
}

func TestBuildPageUsesExtraRowWithoutReturningIt(t *testing.T) {
	type item struct {
		ID   string
		Time time.Time
	}
	items := []item{
		{ID: "018f1234-5678-7abc-8def-012345678901", Time: time.Unix(3, 0)},
		{ID: "018f1234-5678-7abc-8def-012345678902", Time: time.Unix(2, 0)},
		{ID: "018f1234-5678-7abc-8def-012345678903", Time: time.Unix(1, 0)},
	}
	page := BuildPage(items, 2, func(value item) Cursor { return Cursor{Time: value.Time, ID: value.ID} })
	if len(page.Items) != 2 || !page.HasMore || page.NextCursor == "" {
		t.Fatalf("page = %#v", page)
	}
	cursor, err := Decode(page.NextCursor)
	if err != nil || cursor.ID != items[1].ID {
		t.Fatalf("next cursor = %#v, error = %v", cursor, err)
	}
}
