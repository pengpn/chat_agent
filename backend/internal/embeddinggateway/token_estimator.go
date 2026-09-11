package embeddinggateway

import (
	"fmt"
	"sync"
	"unicode"

	"github.com/tiktoken-go/tokenizer"
)

// TokenEstimator estimates one plain-text embedding input before the provider
// request. Provider usage remains the source of truth for final accounting.
type TokenEstimator interface {
	Estimate(text string) int64
}

type HeuristicTokenEstimator struct{}

func (HeuristicTokenEstimator) Estimate(text string) int64 {
	var cjk, other int64
	for _, r := range text {
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r) {
			cjk++
		} else {
			other++
		}
	}
	return cjk + (other+3)/4
}

type tokenizerEstimator struct {
	codec tokenizer.Codec
	mu    sync.Mutex
}

func (e *tokenizerEstimator) Estimate(text string) int64 {
	// Some tokenizer implementations reuse internal buffers. Serializing Count
	// keeps one estimator safe when ingestion workers call it concurrently.
	e.mu.Lock()
	defer e.mu.Unlock()
	count, err := e.codec.Count(text)
	if err != nil {
		return (HeuristicTokenEstimator{}).Estimate(text)
	}
	return int64(count)
}

func NewTokenEstimator(name string) (TokenEstimator, error) {
	if name == "" || name == "heuristic" {
		return HeuristicTokenEstimator{}, nil
	}
	codec, err := tokenizer.Get(tokenizer.Encoding(name))
	if err != nil {
		return nil, fmt.Errorf("load embedding tokenizer %q: %w", name, err)
	}
	return &tokenizerEstimator{codec: codec}, nil
}
