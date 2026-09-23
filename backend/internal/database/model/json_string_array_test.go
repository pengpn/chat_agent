package model

import (
	"reflect"
	"testing"
)

func TestJSONStringArrayValueAndScan(t *testing.T) {
	original := JSONStringArray{"base-1", "base-2"}
	value, err := original.Value()
	if err != nil {
		t.Fatal(err)
	}
	var decoded JSONStringArray
	if err := decoded.Scan(value); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, original) {
		t.Fatalf("decoded = %#v, want %#v", decoded, original)
	}
}

func TestJSONStringArrayRejectsNonStringValues(t *testing.T) {
	var values JSONStringArray
	if err := values.Scan(`["base-1", 2]`); err == nil {
		t.Fatal("expected invalid JSON string array error")
	}
}
