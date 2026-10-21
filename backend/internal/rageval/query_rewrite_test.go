package rageval

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/example/agent-platform/backend/internal/service"
)

func TestQueryRewriteDemoDatasetIsValid(t *testing.T) {
	payload, err := os.ReadFile("../../eval/rag/demo-query-rewrite-dataset.json")
	if err != nil {
		t.Fatal(err)
	}
	dataset, err := DecodeDataset(payload)
	if err != nil || len(dataset.Cases) != 3 || len(dataset.Cases[0].History) != 2 {
		t.Fatalf("查询改写教学样例格式无效: %+v, %v", dataset, err)
	}
}

func rewriteFixture(t *testing.T) (Dataset, Snapshot) {
	t.Helper()
	dataset, run, _ := fixture(t)
	dataset.Cases[0].Query = "它的退款期限是什么？"
	dataset.Cases[0].History = []service.QueryRewriteTurn{{Role: "user", Content: "介绍专业版的售后服务"}}
	run.Cases[0].Query = dataset.Cases[0].Query
	run.Cases[0].HistoryHash = HistoryFingerprint(dataset.Cases[0].History)
	run.Parameters.QueryRewrite = &QueryRewriteParameters{Provider: "synthetic", Model: "small", PromptVersion: service.QueryRewritePromptVersion,
		MaxHistoryMessages: 6, MaxHistoryChars: 6000, MaxInputTokens: 2048, MaxOutputTokens: 256, TimeoutMS: 5000}
	for i := range run.Cases {
		run.Cases[i].RetrievalQuery = run.Cases[i].Query
		run.Cases[i].Rewrite = &service.QueryRewriteObservation{
			Status: "unchanged", Provider: "synthetic", Model: "small", PromptVersion: service.QueryRewritePromptVersion,
			InputHash: "synthetic-input-hash", EstimatedInputTokens: 50, LatencyMS: 20,
			UsageReported: true, InputTokens: 40, OutputTokens: 10, CostKnown: true, CostNanoUSD: 100,
		}
	}
	run.Cases[0].RetrievalQuery = "专业版的退款期限是什么？"
	run.Cases[0].Rewrite.Status = "rewritten"
	run.Cases[0].Rewrite.HistoryMessages = 1
	return dataset, run
}

func TestEvaluateIncludesRewriteLatencyUsageAndFallback(t *testing.T) {
	dataset, run := rewriteFixture(t)
	run.Cases[1].Rewrite = &service.QueryRewriteObservation{Status: "fallback", Reason: "timeout", Provider: "synthetic", Model: "small", PromptVersion: service.QueryRewritePromptVersion, LatencyMS: 80}
	result, err := Evaluate(dataset, run)
	if err != nil {
		t.Fatal(err)
	}
	if result.RewrittenCount != 1 || result.RewriteFallbackCount != 1 || result.MeanRewriteMS != 35 || result.RewriteInputTokens != 120 || result.RewriteOutputTokens != 30 || result.RewriteUsageReportedCases != 3 || result.RewriteKnownCostCases != 3 || result.RewriteCostNanoUSD != 300 {
		t.Fatalf("改写指标不完整: %+v", result)
	}
	if result.Cases[0].RetrievalQuery != "专业版的退款期限是什么？" || result.Cases[0].RewriteStatus != "rewritten" {
		t.Fatal("报告应保留真正用于检索的问题")
	}
	// 禁用实验仍使用完全相同的原问题和历史，不允许换题比较。
	baseline := run
	baseline.Name = "rewrite-disabled-baseline"
	baseline.Parameters.QueryRewrite = nil
	baseline.Cases = append([]Observation(nil), run.Cases...)
	for i := range baseline.Cases {
		baseline.Cases[i].Rewrite = nil
		baseline.Cases[i].RetrievalQuery = baseline.Cases[i].Query
	}
	if _, err := Compare(dataset, []Snapshot{baseline, run}); err != nil {
		t.Fatal(err)
	}
}

func TestEvaluateRejectsInconsistentRewriteEvidence(t *testing.T) {
	dataset, run := rewriteFixture(t)
	for _, test := range []struct {
		name   string
		mutate func(*Snapshot)
	}{
		{name: "历史变更", mutate: func(r *Snapshot) { r.Cases[0].HistoryHash = "different" }},
		{name: "模型变更", mutate: func(r *Snapshot) { r.Cases[0].Rewrite.Model = "other" }},
		{name: "未记录观测", mutate: func(r *Snapshot) { r.Cases[0].Rewrite = nil }},
		{name: "无效耗时", mutate: func(r *Snapshot) { r.Cases[0].Rewrite.LatencyMS = -1 }},
		{name: "失败却换题", mutate: func(r *Snapshot) { r.Cases[0].Rewrite.Status = "fallback"; r.Cases[0].Rewrite.Reason = "timeout" }},
		{name: "超预算仍调用", mutate: func(r *Snapshot) { r.Cases[0].Rewrite.EstimatedInputTokens = 10000 }},
		{name: "未知用量伪装零价", mutate: func(r *Snapshot) { r.Cases[0].Rewrite.UsageReported = false }},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload, _ := json.Marshal(run)
			var changed Snapshot
			_ = json.Unmarshal(payload, &changed)
			test.mutate(&changed)
			if err := ValidateSnapshot(dataset, changed); err == nil {
				t.Fatal("不一致的实验记录必须拒绝")
			}
		})
	}
	dataset.Cases[0].History[0].Role = "system"
	if err := ValidateDataset(dataset); err == nil {
		t.Fatal("评估集历史不能包含系统角色")
	}
}
