package rageval

import (
	"encoding/json"
	"math"
	"os"
	"testing"
)

func fixture(t *testing.T) (Dataset, Snapshot, Snapshot) {
	t.Helper()
	data, err := os.ReadFile("../../eval/rag/demo-dataset.json")
	if err != nil {
		t.Fatal(err)
	}
	dataset, err := DecodeDataset(data)
	if err != nil {
		t.Fatal(err)
	}
	readSnapshot := func(path string) Snapshot {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var snapshot Snapshot
		if err := json.Unmarshal(data, &snapshot); err != nil {
			t.Fatal(err)
		}
		return snapshot
	}
	return dataset, readSnapshot("../../eval/rag/demo-baseline.json"), readSnapshot("../../eval/rag/demo-top3.json")
}

func TestEvaluateSeparatesRetrievalSelectionGenerationAndNoEvidence(t *testing.T) {
	dataset, baseline, top3 := fixture(t)
	reports, err := Compare(dataset, []Snapshot{baseline, top3})
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 2 || reports[0].AnswerableCount != 3 || reports[0].UnanswerableCount != 1 {
		t.Fatalf("unexpected report counts: %+v", reports)
	}
	assertNear(t, reports[0].CandidateRecall, 2.0/3)
	assertNear(t, reports[0].RecallAtK, 0.5)
	assertNear(t, reports[1].RecallAtK, 2.0/3)
	assertNear(t, reports[0].MRRAtK, 0.5)
	assertNear(t, *reports[0].Faithfulness, 0.75)
	assertNear(t, *reports[0].CitationPrecision, 0.75)
	assertNear(t, *reports[0].NoEvidenceAccuracy, 1)
	if reports[0].Cases[1].Outcome != "candidate_miss" || reports[0].Cases[2].Outcome != "generation_or_citation_error" {
		t.Fatalf("did not distinguish retrieval from answer failures: %+v", reports[0].Cases)
	}
	if reports[1].NDCGAtK <= reports[0].NDCGAtK {
		t.Fatal("top-3 should improve ranking coverage in the synthetic fixture")
	}
}

func TestEvaluateRejectsUnreviewedOrMismatchedRuns(t *testing.T) {
	dataset, baseline, top3 := fixture(t)
	baseline.Cases[0].Claims[0].Text = "not in answer"
	if _, err := Evaluate(dataset, baseline); err == nil {
		t.Fatal("unanchored claim must be rejected")
	}
	dataset, baseline, _ = fixture(t)
	baseline.Cases[0].AnswerCorrect = nil
	if _, err := Evaluate(dataset, baseline); err == nil {
		t.Fatal("generation labels without review must be rejected")
	}
	dataset, baseline, _ = fixture(t)
	top3.CorpusFingerprint = "other corpus"
	if _, err := Compare(dataset, []Snapshot{baseline, top3}); err == nil {
		t.Fatal("different corpora must not be compared")
	}
}

func TestCitationScoresRequireExpectedAndSelectedDocument(t *testing.T) {
	precision, recall := citationScores([]string{"wrong", "right", "right"}, []string{"right"}, map[string]bool{"right": true})
	assertNear(t, *precision, 0.5)
	assertNear(t, *recall, 1)
	precision, recall = citationScores([]string{"right"}, []string{"right"}, map[string]bool{})
	assertNear(t, *precision, 0)
	assertNear(t, *recall, 0)
}

func TestEvaluateCountsInventedCitationMarker(t *testing.T) {
	dataset, baseline, _ := fixture(t)
	baseline.Cases[0].Answer = "收到商品后七天内可申请退款。[9]"
	baseline.Cases[0].CitedDocumentIDs = nil
	report, err := Evaluate(dataset, baseline)
	if err != nil {
		t.Fatal(err)
	}
	if *report.Cases[0].CitationPrecision != 0 || *report.Cases[0].CitationRecall != 0 || report.Cases[0].Outcome != "generation_or_citation_error" {
		t.Fatalf("invented marker should fail citation grading: %+v", report.Cases[0])
	}
	baseline.Cases[0].CitedDocumentIDs = []string{"doc-refund"}
	if _, err := Evaluate(dataset, baseline); err == nil {
		t.Fatal("citation annotation must agree with answer markers")
	}
}

func TestEvaluateDetectsSelectionMissAfterThresholdChange(t *testing.T) {
	dataset, baseline, _ := fixture(t)
	baseline.Parameters.SimilarityThreshold = 0.9
	for i := range baseline.Cases {
		baseline.Cases[i].Answer = ""
		baseline.Cases[i].AnswerCorrect = nil
		baseline.Cases[i].Claims = nil
		baseline.Cases[i].CitedDocumentIDs = nil
		baseline.Cases[i].GenerationMS = 0
	}
	report, err := Evaluate(dataset, baseline)
	if err != nil {
		t.Fatal(err)
	}
	if report.GenerationGraded != 0 || report.Faithfulness != nil || report.Cases[0].Outcome != "selection_miss" {
		t.Fatalf("selection miss or ungraded answer lost: %+v", report)
	}
}

func TestFingerprintSourceDocumentsIgnoresInputOrder(t *testing.T) {
	if FingerprintSourceDocuments([]string{"b", "a"}) != FingerprintSourceDocuments([]string{"a", "b"}) {
		t.Fatal("fingerprint should be order independent")
	}
}

func TestHybridEvaluationRebuildsRRFAndRetainsKeywordOnlyCandidates(t *testing.T) {
	dataset, baseline, _ := fixture(t)
	data, err := os.ReadFile("../../eval/rag/demo-hybrid.json")
	if err != nil {
		t.Fatal(err)
	}
	var hybrid Snapshot
	if err := json.Unmarshal(data, &hybrid); err != nil {
		t.Fatal(err)
	}
	reports, err := Compare(dataset, []Snapshot{baseline, hybrid})
	if err != nil {
		t.Fatal(err)
	}
	assertNear(t, reports[1].RecallAtK, 1)
	if reports[1].GenerationGraded != 0 || reports[1].MeanKeywordSearchMS != 2 || reports[1].Cases[1].SelectedChunkIDs[0] != "invoice-1" {
		t.Fatalf("混合候选重放失败: %+v", reports[1])
	}
	hybrid.Cases[0].Candidates[0].VectorRank = 10
	if _, err := Evaluate(dataset, hybrid); err == nil {
		t.Fatal("候选排名必须在每路候选上限以内")
	}
}

func assertNear(t *testing.T, actual, expected float64) {
	t.Helper()
	if math.Abs(actual-expected) > 1e-9 {
		t.Fatalf("actual %v, want %v", actual, expected)
	}
}
