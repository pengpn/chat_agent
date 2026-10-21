package config

import (
	"fmt"
	"strings"
	"time"
)

// QueryRewriteConfig 使用已有的对话 Provider，但可以选择独立的小模型。
// 开关与预算均由服务端管理，不允许网页任意指定改写模型或提示词。
type QueryRewriteConfig struct {
	Enabled            bool
	Provider           string
	Model              string
	MaxHistoryMessages int
	MaxHistoryChars    int
	MaxInputTokens     int64
	MaxOutputTokens    int64
	Timeout            time.Duration
}

func loadQueryRewriteConfig() (QueryRewriteConfig, error) {
	cfg := QueryRewriteConfig{
		Provider: strings.TrimSpace(env("RAG_QUERY_REWRITE_PROVIDER", "")),
		Model:    strings.TrimSpace(env("RAG_QUERY_REWRITE_MODEL", "")),
	}
	var err error
	if cfg.Enabled, err = boolEnv("RAG_QUERY_REWRITE_ENABLED", false); err != nil {
		return cfg, err
	}
	if cfg.MaxHistoryMessages, err = positiveIntEnv("RAG_QUERY_REWRITE_MAX_HISTORY_MESSAGES", 6); err != nil {
		return cfg, err
	}
	if cfg.MaxHistoryChars, err = positiveIntEnv("RAG_QUERY_REWRITE_MAX_HISTORY_CHARS", 6000); err != nil {
		return cfg, err
	}
	if cfg.MaxInputTokens, err = positiveInt64Env("RAG_QUERY_REWRITE_MAX_INPUT_TOKENS", 2048); err != nil {
		return cfg, err
	}
	if cfg.MaxOutputTokens, err = positiveInt64Env("RAG_QUERY_REWRITE_MAX_OUTPUT_TOKENS", 256); err != nil {
		return cfg, err
	}
	if cfg.Timeout, err = positiveDurationEnv("RAG_QUERY_REWRITE_TIMEOUT", "5s"); err != nil {
		return cfg, err
	}
	if cfg.Timeout < time.Millisecond {
		return cfg, fmt.Errorf("RAG_QUERY_REWRITE_TIMEOUT 不能小于 1ms")
	}
	if cfg.MaxHistoryMessages > 50 || cfg.MaxHistoryChars > 50000 {
		return cfg, fmt.Errorf("查询改写历史最多允许 50 条消息、50000 个字符")
	}
	if cfg.Enabled && (cfg.Provider == "" || cfg.Model == "") {
		return cfg, fmt.Errorf("启用查询改写时必须配置 RAG_QUERY_REWRITE_PROVIDER 和 RAG_QUERY_REWRITE_MODEL")
	}
	return cfg, nil
}
