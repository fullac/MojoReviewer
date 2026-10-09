package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFromHiddenConfig(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeConfig(t, `{
	  "log_level": "debug",
	  "server": {"host": "0.0.0.0", "port": 9970},
	  "github": {"webhook_secret": "hook", "token": "gh_test", "repos": ["acme/web", " acme/api "], "trigger_user": "@mojo"},
	  "agent": {"provider": "anthropic", "model": "claude-opus-4-6", "api_key": "sk-test", "api_base": "https://example.test", "task_timeout": "5m"}
	}`)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LogLevel != "debug" || cfg.Server.Addr() != "0.0.0.0:9970" {
		t.Fatalf("服务配置错误: %+v", cfg.Server)
	}
	if cfg.GitHub.Token != "gh_test" || cfg.GitHub.TriggerUser != "mojo" || len(cfg.GitHub.Repos) != 2 {
		t.Fatalf("github 配置错误: %+v", cfg.GitHub)
	}
	if cfg.Agent.Provider != "anthropic" || cfg.Agent.Model != "claude-opus-4-6" || cfg.Agent.APIKey != "sk-test" {
		t.Fatalf("模型配置错误: %+v", cfg.Agent)
	}
	if cfg.Agent.TaskTimeout.String() != "5m0s" {
		t.Fatalf("超时错误: %s", cfg.Agent.TaskTimeout)
	}
	if cfg.Agent.HomeDir != filepath.Join(cfg.DataDir, "luckyagent") {
		t.Fatalf("HomeDir 应落在数据目录下: %s", cfg.Agent.HomeDir)
	}
}

func TestLoadRejectsMissingSecret(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeConfig(t, `{"github":{"webhook_secret":"hook"},"agent":{"api_key":""}}`)
	if _, err := Load(); err == nil {
		t.Fatal("缺少模型密钥时应失败")
	}
}

func writeConfig(t *testing.T, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(ConfigPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ConfigPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
