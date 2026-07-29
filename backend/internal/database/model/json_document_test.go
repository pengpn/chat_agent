package model

import (
	"encoding/json"
	"testing"
)

func TestJSONDocumentPreservesJSONType(t *testing.T) {
	var document JSONDocument
	if err := document.Scan([]byte(`{"answer":"ok"}`)); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(struct {
		Value JSONDocument `json:"value"`
	}{Value: document})
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"value":{"answer":"ok"}}` {
		t.Fatalf("encoded = %s", encoded)
	}
}
