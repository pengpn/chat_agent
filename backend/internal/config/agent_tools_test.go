package config

import "testing"

func TestAgentToolConfigFailsClosedForSearchAndBoundsHumanTimeout(t *testing.T) {
	t.Setenv("WEB_SEARCH_ENABLED", "true")
	t.Setenv("WEB_SEARCH_API_KEY", "")
	if _, err := loadAgentToolsConfig(); err == nil {
		t.Fatal("搜索无密钥不能启用")
	}
	t.Setenv("WEB_SEARCH_API_KEY", "test-only")
	t.Setenv("TOOL_HUMAN_TIMEOUT", "10m")
	if _, err := loadAgentToolsConfig(); err == nil {
		t.Fatal("人工等待超时不能无限延长")
	}
	t.Setenv("TOOL_HUMAN_TIMEOUT", "90s")
	t.Setenv("WEB_SEARCH_MAX_RESULTS", "11")
	if _, err := loadAgentToolsConfig(); err == nil {
		t.Fatal("搜索结果数超限未拦截")
	}
	t.Setenv("WEB_SEARCH_MAX_RESULTS", "5")
	if _, err := loadAgentToolsConfig(); err != nil {
		t.Fatal(err)
	}
}
