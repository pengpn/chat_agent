package config

import "testing"

func TestRerankConfigurationIsOptInAndValidated(t *testing.T) {
	t.Setenv("RERANK_ENABLED", "false")
	t.Setenv("RERANK_ENDPOINT", "")
	t.Setenv("RERANK_MODEL", "")
	cfg, err := loadRerankConfig()
	if err != nil || cfg.Enabled || cfg.MaxDocuments != 60 {
		t.Fatalf("关闭时应允许空端点: %+v,%v", cfg, err)
	}
	t.Setenv("RERANK_ENABLED", "true")
	if _, err := loadRerankConfig(); err == nil {
		t.Fatal("开启时必须配置端点")
	}
	t.Setenv("RERANK_ENDPOINT", "http://localhost:8081/v1/rerank")
	t.Setenv("RERANK_MODEL", "rank-local")
	if _, err := loadRerankConfig(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RERANK_ENDPOINT", "https://user:secret@example.com/rerank")
	if _, err := loadRerankConfig(); err == nil {
		t.Fatal("端点不能包含凭据")
	}
	t.Setenv("RERANK_ENDPOINT", "http://localhost/rerank")
	t.Setenv("RERANK_MAX_DOCUMENTS", "401")
	if _, err := loadRerankConfig(); err == nil {
		t.Fatal("候选数量不能超出已知上限")
	}
}
