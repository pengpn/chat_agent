package config

import "testing"

func TestExecutionConfigDefaultsAndLimits(t *testing.T) {
	t.Setenv("EXECUTION_ENABLED", "false")
	cfg, err := loadExecutionConfig()
	if err != nil || cfg.Enabled || !cfg.AdminOnly || cfg.MaxOutputBytes != 4096 {
		t.Fatalf("默认应关闭并限制为管理员: %+v,%v", cfg, err)
	}
	for _, item := range []struct{ key, value string }{{"EXECUTION_MEMORY_MB", "32"}, {"EXECUTION_MAX_ENTRIES", "10000"}, {"EXECUTION_MAX_FILE_BYTES", "999999999"}, {"EXECUTION_IMAGE", "--privileged"}, {"EXECUTION_TIMEOUT", "5m"}} {
		t.Run(item.key, func(t *testing.T) {
			t.Setenv(item.key, item.value)
			if _, err := loadExecutionConfig(); err == nil {
				t.Fatal("危险配置必须拒绝")
			}
		})
	}
}
