package embeddinggateway

import "testing"

func TestEmbeddingTokenEstimators(t *testing.T) {
	heuristic := HeuristicTokenEstimator{}
	if got := heuristic.Estimate("中文abcde"); got != 4 {
		t.Fatalf("heuristic estimate=%d want=4", got)
	}
	estimator, err := NewTokenEstimator("cl100k_base")
	if err != nil {
		t.Fatal(err)
	}
	if got := estimator.Estimate("hello embeddings"); got <= 0 {
		t.Fatalf("tokenizer estimate=%d", got)
	}
	if _, err := NewTokenEstimator("not-a-tokenizer"); err == nil {
		t.Fatal("expected unsupported tokenizer to fail")
	}
}
