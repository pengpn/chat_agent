package config

import (
	"fmt"
	"time"
)

type AgentToolsConfig struct {
	HumanTimeout     time.Duration
	SearchEnabled    bool
	SearchAPIKey     string
	SearchTimeout    time.Duration
	SearchMaxResults int
}

func loadAgentToolsConfig() (AgentToolsConfig, error) {
	c := AgentToolsConfig{SearchAPIKey: env("WEB_SEARCH_API_KEY", "")}
	var err error
	if c.HumanTimeout, err = positiveDurationEnv("TOOL_HUMAN_TIMEOUT", "90s"); err != nil {
		return c, err
	}
	if c.SearchEnabled, err = boolEnv("WEB_SEARCH_ENABLED", false); err != nil {
		return c, err
	}
	if c.SearchTimeout, err = positiveDurationEnv("WEB_SEARCH_TIMEOUT", "10s"); err != nil {
		return c, err
	}
	if c.SearchMaxResults, err = positiveIntEnv("WEB_SEARCH_MAX_RESULTS", 5); err != nil {
		return c, err
	}
	if c.HumanTimeout > 5*time.Minute || c.SearchTimeout > 30*time.Second || c.SearchMaxResults > 10 {
		return c, fmt.Errorf("MVP 工具超时或搜索条数超过安全上限")
	}
	if c.SearchEnabled && c.SearchAPIKey == "" {
		return c, fmt.Errorf("启用联网搜索必须配置 WEB_SEARCH_API_KEY")
	}
	return c, nil
}
