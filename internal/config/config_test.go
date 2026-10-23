package config

import (
	"bytes"
	"fmt"
	"log/slog"
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
	if cfg.Agent.FixEnabled {
		t.Fatal("未配置时 fix_enabled 应为 false")
	}
}

func TestLoadFixEnabled(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeConfig(t, `{
	  "github": {"webhook_secret": "hook"},
	  "agent": {"api_key": "sk-test", "fix_enabled": true}
	}`)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Agent.FixEnabled {
		t.Fatal("fix_enabled=true 未生效")
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
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
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

func TestLoadFixSecurityOptions(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeConfig(t, `{
	  "github": {"webhook_secret": "hook"},
	  "agent": {
	    "api_key": "sk-test",
	    "fix_enabled": true,
	    "fix_push_enabled": false,
	    "fix_allowed_authors": ["@Ada", "bob", "ADA"],
	    "fix_max_changed_files": 12,
	    "fix_max_diff_bytes": 1000
	  }
	}`)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Agent.FixEnabled || cfg.Agent.FixPushEnabled {
		t.Fatalf("push 应显式关闭: %+v", cfg.Agent)
	}
	if got := strings.Join(cfg.Agent.FixAllowedAuthors, ","); got != "ada,bob" {
		t.Fatalf("authors=%v", cfg.Agent.FixAllowedAuthors)
	}
	if cfg.Agent.FixMaxChangedFiles != 12 || cfg.Agent.FixMaxDiffBytes != 1000 {
		t.Fatalf("limits=%d %d", cfg.Agent.FixMaxChangedFiles, cfg.Agent.FixMaxDiffBytes)
	}
}

func TestLoadFixPushDefaultsWithFix(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeConfig(t, `{
	  "github": {"webhook_secret": "hook"},
	  "agent": {"api_key": "sk-test", "fix_enabled": true}
	}`)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Agent.FixPushEnabled {
		t.Fatal("fix_enabled 时默认应允许 push")
	}
}

func TestManagedContextAndRequestTimeout(t *testing.T) {
	t.Chdir(t.TempDir())
	writeConfig(t, `{"agent":{"api_key":"fixture","max_context_tokens":40000,"request_timeout":"300s"},"github":{"webhook_secret":"fixture"}}`)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.MaxContextTokens != 40000 || cfg.Agent.RequestTimeout.String() != "5m0s" {
		t.Fatal("limits not applied")
	}
	for _, fields := range []string{`"max_context_tokens":-1`, `"request_timeout":"zero"`, `"request_timeout":"0s"`} {
		writeConfig(t, `{"agent":{"api_key":"fixture",`+fields+`},"github":{"webhook_secret":"fixture"}}`)
		if _, err := Load(); err == nil {
			t.Fatal("invalid limit accepted")
		}

	}
}

func TestLogLevelValidation(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, level := range []string{"debug", "info", "warn", "error", " DEBUG "} {
		writeConfig(t, `{"log_level":"`+level+`","agent":{"api_key":"fixture"},"github":{"webhook_secret":"fixture"}}`)
		cfg, err := Load()
		if err != nil || cfg.LogLevel != strings.ToLower(strings.TrimSpace(level)) {
			t.Fatalf("level=%q normalized=%q err=%v", level, cfg.LogLevel, err)
		}
	}
	writeConfig(t, `{"log_level":"trace","agent":{"api_key":"fixture"},"github":{"webhook_secret":"fixture"}}`)
	if _, err := Load(); err == nil {
		t.Fatal("invalid log_level accepted")
	}
}

func TestReviewConcurrencyDefaultsAndRange(t *testing.T) {
	t.Chdir(t.TempDir())
	writeConfig(t, `{"agent":{"api_key":"fixture"},"github":{"webhook_secret":"fixture"}}`)
	cfg, err := Load()
	if err != nil || cfg.Agent.ReviewConcurrency != 2 {
		t.Fatalf("default concurrency=%d err=%v", cfg.Agent.ReviewConcurrency, err)
	}
	for _, value := range []int{1, 4} {
		writeConfig(t, fmt.Sprintf(`{"agent":{"api_key":"fixture","review_concurrency":%d},"github":{"webhook_secret":"fixture"}}`, value))
		cfg, err = Load()
		if err != nil || cfg.Agent.ReviewConcurrency != value {
			t.Fatalf("concurrency=%d normalized=%d err=%v", value, cfg.Agent.ReviewConcurrency, err)
		}
	}
	for _, value := range []int{-1, 5} {
		writeConfig(t, fmt.Sprintf(`{"agent":{"api_key":"fixture","review_concurrency":%d},"github":{"webhook_secret":"fixture"}}`, value))
		if _, err := Load(); err == nil {
			t.Fatalf("invalid concurrency %d accepted", value)
		}
	}
}
