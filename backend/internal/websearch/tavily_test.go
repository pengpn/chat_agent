package websearch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTavilyRequestAndBoundedUntrustedResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("认证或方法错误")
		}
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if payload["include_raw_content"] != false || payload["auto_parameters"] != false || payload["max_results"] != float64(2) {
			t.Errorf("请求不受限: %+v", payload)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []Result{{"bad", "javascript:alert(1)", "bad"}, {"bad", "https://user:pass@example.com/", "bad"}, {"官方文档", "https://example.com/docs", strings.Repeat("中", 2000)}, {"other", "https://example.org/", "ok"}, {"extra", "https://example.net/", "not returned"}}})
	}))
	defer server.Close()
	client := NewTavily("secret", 2, time.Second)
	client.endpoint = server.URL
	results, err := client.Search(context.Background(), "Golang")
	if err != nil || len(results) != 2 || len([]rune(results[0].Content)) != 800 {
		t.Fatalf("结果边界错误: %+v %v", results, err)
	}
}
func TestTavilyDoesNotLeakUpstreamBodyOrFollowRedirects(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://example.com/")
		w.WriteHeader(http.StatusFound)
		_, _ = w.Write([]byte("secret sensitive upstream detail"))
	}))
	defer server.Close()
	client := NewTavily("secret", 2, time.Second)
	client.endpoint = server.URL
	_, err := client.Search(context.Background(), "query")
	if err == nil || strings.Contains(err.Error(), "secret") || !strings.Contains(err.Error(), "302") {
		t.Fatalf("错误未脱敏/重定向未拒绝: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Search(ctx, "query"); err != context.Canceled {
		t.Fatalf("未透传取消: %v", err)
	}
}
