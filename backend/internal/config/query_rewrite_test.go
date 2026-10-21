package config

import "testing"

func TestQueryRewriteConfigIsOptInAndRequiresEnabledModel(t *testing.T) {
	t.Setenv("RAG_QUERY_REWRITE_ENABLED", "false")
	t.Setenv("RAG_QUERY_REWRITE_PROVIDER", "")
	t.Setenv("RAG_QUERY_REWRITE_MODEL", "")
	cfg, err := loadQueryRewriteConfig()
	if err != nil || cfg.Enabled || cfg.MaxHistoryMessages != 6 || cfg.MaxInputTokens != 2048 {
		t.Fatalf("默认应关闭并提供有界预算: %+v, %v", cfg, err)
	}
	t.Setenv("RAG_QUERY_REWRITE_ENABLED", "true")
	if _, err := loadQueryRewriteConfig(); err == nil {
		t.Fatal("开启时不能缺少模型")
	}
	t.Setenv("RAG_QUERY_REWRITE_PROVIDER", "demo")
	t.Setenv("RAG_QUERY_REWRITE_MODEL", "small")
	cfg, err = loadQueryRewriteConfig()
	if err != nil {
		t.Fatal(err)
	}
	if err := validateOptionalModelTarget("RAG_QUERY_REWRITE", cfg.Provider, cfg.Model, nil); err == nil {
		t.Fatal("模型必须已在服务端注册并配置密钥")
	}
	if err := validateOptionalModelTarget("RAG_QUERY_REWRITE", cfg.Provider, cfg.Model, []ProviderConfig{{ID: "demo", Models: []string{"small"}}}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RAG_QUERY_REWRITE_MAX_HISTORY_MESSAGES", "51")
	if _, err := loadQueryRewriteConfig(); err == nil {
		t.Fatal("历史条数必须有上限")
	}
	t.Setenv("RAG_QUERY_REWRITE_MAX_HISTORY_MESSAGES", "6")
	t.Setenv("RAG_QUERY_REWRITE_MAX_INPUT_TOKENS", "0")
	if _, err := loadQueryRewriteConfig(); err == nil {
		t.Fatal("输入预算必须为正数")
	}
}
