package config

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadFromHiddenConfig(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeConfig(t, `{
	  "log_level": "debug",
	  "server": {"host": "0.0.0.0", "port": 9970},
	  "github": {"host": "ghe.example.com", "webhook_secret": "hook", "token": "gh_test", "repos": ["acme/web", " acme/api "], "trigger_user": "@mojo"},
	  "agent": {"provider": "anthropic", "model": "claude-opus-4-6", "api_key": "sk-test", "api_base": "https://example.test", "task_timeout": "5m"}
	}`)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LogLevel != "debug" || cfg.Server.Addr() != "0.0.0.0:9970" {
		t.Fatalf("服务配置错误: %+v", cfg.Server)
	}
	if cfg.GitHub.Host != "ghe.example.com" || cfg.GitHub.Token != "gh_test" || cfg.GitHub.TriggerUser != "mojo" || len(cfg.GitHub.Repos) != 2 {
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

func TestGitHubHost(t *testing.T) {
	if got := githubHost("  github.example.com  "); got != "github.example.com" {
		t.Fatalf("主机名应保留: %q", got)
	}
	if got := githubHost("   "); got != "" {
		t.Fatalf("空主机应保持为空: %q", got)
	}
	for _, raw := range []string{"https://github.example.com", "github.example.com/api", "github.example.com:443", "host name", "user@host"} {
		if got := githubHost(raw); got != "" {
			t.Fatalf("%q 应忽略，得到 %q", raw, got)
		}
	}
}

func TestLoadWarnsOnInvalidGitHubHost(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeConfig(t, `{"github":{"host":"https://ghe.example.com","webhook_secret":"hook"},"agent":{"api_key":"sk-test"}}`)
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GitHub.Host != "" {
		t.Fatalf("非法主机应被忽略: %q", cfg.GitHub.Host)
	}
	if !strings.Contains(buf.String(), "https://ghe.example.com") || !strings.Contains(buf.String(), "已忽略") {
		t.Fatalf("非法主机没有警告: %s", buf.String())
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
