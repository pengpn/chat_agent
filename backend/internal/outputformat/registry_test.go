package outputformat

import "testing"

func TestDefaultRegistryReturnsDefensiveSchemaCopy(t *testing.T) {
	registry := NewDefaultRegistry()
	first, ok := registry.Get("contact_extraction")
	if !ok {
		t.Fatal("contact_extraction is not registered")
	}
	first.Schema[0] = '['
	second, ok := registry.Get("contact_extraction")
	if !ok || len(second.Schema) == 0 || second.Schema[0] != '{' {
		t.Fatalf("registry schema was mutated: %s", second.Schema)
	}
}
