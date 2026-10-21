package rageval

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/example/agent-platform/backend/internal/service"
)

// HistoryFingerprint 防止两个实验使用相同问题，却偷偷换了指代所依据的历史。
func HistoryFingerprint(history []service.QueryRewriteTurn) string {
	if len(history) == 0 {
		return ""
	}
	payload, _ := json.Marshal(history)
	hash := sha256.Sum256(payload)
	return hex.EncodeToString(hash[:])
}

func validateRewriteObservation(gold Case, observed Observation, parameters *QueryRewriteParameters) error {
	if observed.HistoryHash != HistoryFingerprint(gold.History) {
		return fmt.Errorf("rewrite history fingerprint does not match dataset")
	}
	if parameters == nil {
		if observed.RetrievalQuery != "" && observed.RetrievalQuery != strings.TrimSpace(gold.Query) {
			return fmt.Errorf("disabled rewrite cannot change retrieval query")
		}
		if observed.Rewrite != nil {
			if *observed.Rewrite != (service.QueryRewriteObservation{Status: "disabled"}) {
				return fmt.Errorf("disabled rewrite has an active observation")
			}
		}
		return nil
	}
	r := observed.Rewrite
	if r == nil || strings.TrimSpace(observed.RetrievalQuery) == "" || r.Provider != parameters.Provider || r.Model != parameters.Model || r.PromptVersion != parameters.PromptVersion {
		return fmt.Errorf("missing or mismatched rewrite observation")
	}
	if r.LatencyMS < 0 || r.EstimatedInputTokens < 0 || r.InputTokens < 0 || r.OutputTokens < 0 || r.CostNanoUSD < 0 || r.HistoryMessages < 0 || r.HistoryMessages > parameters.MaxHistoryMessages {
		return fmt.Errorf("invalid rewrite usage or latency")
	}
	if (!r.UsageReported && (r.InputTokens != 0 || r.OutputTokens != 0 || r.CostKnown)) || (!r.CostKnown && r.CostNanoUSD != 0) {
		return fmt.Errorf("rewrite usage and cost flags are inconsistent")
	}
	switch r.Status {
	case "rewritten", "unchanged":
		if r.InputHash == "" || r.EstimatedInputTokens > parameters.MaxInputTokens {
			return fmt.Errorf("rewrite admission evidence is missing")
		}
	case "fallback":
		if r.Reason == "" {
			return fmt.Errorf("rewrite fallback reason is missing")
		}
	default:
		return fmt.Errorf("invalid rewrite status %q", r.Status)
	}
	if r.Status != "rewritten" && observed.RetrievalQuery != strings.TrimSpace(gold.Query) {
		return fmt.Errorf("unchanged or failed rewrite must use original query")
	}
	return nil
}
