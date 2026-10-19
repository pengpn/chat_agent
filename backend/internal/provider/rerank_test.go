package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/example/agent-platform/backend/internal/config"
)

func TestCompatibleRerankProtocolAndOptionalUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/rerank" || r.Header.Get("Authorization") != "Bearer test-only" {
			t.Errorf("请求路径或鉴权错误")
		}
		var body struct {
			Model     string   `json:"model"`
			Query     string   `json:"query"`
			Documents []string `json:"documents"`
			TopN      int      `json:"top_n"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Model != "rank-v1" || body.Query != "退款" || body.TopN != 2 || len(body.Documents) != 2 {
			t.Errorf("请求正文错误: %+v", body)
		}
		_, _ = w.Write([]byte(`{"model":"rank-v1","results":[{"index":1,"relevance_score":0.9},{"index":0,"relevance_score":0}],"usage":{"total_tokens":42},"meta":{"billed_units":{"search_units":1}}}`))
	}))
	defer server.Close()
	client := NewCompatibleRerank(config.RerankConfig{Endpoint: server.URL + "/v1/rerank", APIKey: "test-only"})
	result, err := client.Rerank(context.Background(), RerankRequest{Model: "rank-v1", Query: "退款", Documents: []string{"a", "b"}})
	if err != nil || len(result.Items) != 2 || result.Items[1].Score != 0 || result.InputTokens == nil || *result.InputTokens != 42 || result.BilledSearchUnits == nil || *result.BilledSearchUnits != 1 {
		t.Fatalf("响应解析错误: %+v,%v", result, err)
	}
}

func TestCompatibleRerankRejectsMissingFieldsAndDoesNotExposeErrorBody(t *testing.T) {
	for _, body := range []string{`{"results":[{"index":0}]}`, `{"results":[{"relevance_score":0.1}]}`, `not-json`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
		client := NewCompatibleRerank(config.RerankConfig{Endpoint: server.URL})
		_, err := client.Rerank(context.Background(), RerankRequest{Documents: []string{"a"}})
		server.Close()
		if err == nil {
			t.Fatalf("应拒绝缺失字段: %s", body)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		_, _ = w.Write([]byte("private upstream content"))
	}))
	defer server.Close()
	_, err := NewCompatibleRerank(config.RerankConfig{Endpoint: server.URL}).Rerank(context.Background(), RerankRequest{})
	if err == nil || strings.Contains(err.Error(), "private upstream content") || !strings.Contains(err.Error(), "429") {
		t.Fatalf("HTTP 错误处理错误: %v", err)
	}
}

func TestCompatibleRerankCancelsHTTPCall(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		_, err := NewCompatibleRerank(config.RerankConfig{Endpoint: server.URL}).Rerank(ctx, RerankRequest{Model: "rank-v1", Query: "q", Documents: []string{"d"}})
		finished <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("请求没有到达本地服务")
	}
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("HTTP 取消未传播: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("HTTP 请求没有及时取消")
	}
}
