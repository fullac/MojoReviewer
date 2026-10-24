package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mojoreviewer/mojoreviewer/internal/config"
)

func TestIsolatedRuntimeKeepsConfiguredModelAndLimits(t *testing.T) {
	dir := t.TempDir()
	cfg := config.AgentConfig{HomeDir: dir, Provider: "openai-compatible", Model: "fixture-model", APIKey: "fixture-key", MaxContextTokens: 40000, RequestTimeout: 300 * time.Second}
	if err := writeRuntimeConfig(cfg); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatal("runtime credentials not private")
	}
	raw, _ := os.ReadFile(path)
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	if data["context"].(map[string]any)["max_context_tokens"] != float64(40000) {
		t.Fatal("context lost")
	}
	if data["limits"].(map[string]any)["timeout_seconds"] != float64(300) {
		t.Fatal("request timeout lost")
	}
	active := data["models"].(map[string]any)["active"].(map[string]any)
	if active["chat"] != "fixture-model" || active["compact"] != "fixture-model" {
		t.Fatal("compaction switches model")
	}
}

func TestSDKLoadsManagedModelContextAndTimeout(t *testing.T) {
	cfg := config.AgentConfig{HomeDir: t.TempDir(), Provider: "openai", Model: "gpt-test", APIKey: "fixture-value", APIBase: "http://127.0.0.1:1/v1", MaxContextTokens: 40000, RequestTimeout: 5 * time.Minute, TaskTimeout: 20 * time.Minute}
	runner, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()
	got := runner.agent.Underlying().Config().Get()
	if got.Context.MaxContextTokens != 40000 || got.Limits.TimeoutSeconds != 300 || got.Agent.TimeoutSeconds != 300 {
		t.Fatalf("effective context or timeout differs from config")
	}
	if got.Agent.Foreground.Enabled == nil || *got.Agent.Foreground.Enabled || got.Agent.MaxIterations != 30 {
		t.Fatal("SDK did not load the host-managed ordinary loop")
	}
	if got.LlmProvider.Model != "gpt-test" || got.LlmProvider.Name != "openai" {
		t.Fatal("SDK replaced configured model")
	}
	if got.Models.Active["chat"] != cfg.Model || got.Models.Active["compact"] != cfg.Model || got.Context.CompactModel != cfg.Model {
		t.Fatal("SDK selected a different model for chat or compaction")
	}
	chat, compact := got.Models.Endpoints["chat"], got.Models.Endpoints["compact"]
	if chat.Provider != cfg.Provider || compact.Provider != cfg.Provider || chat.APIBase != cfg.APIBase || compact.APIBase != cfg.APIBase || chat.APIKey != cfg.APIKey || compact.APIKey != cfg.APIKey {
		t.Fatal("SDK replaced the configured chat or compaction endpoint")
	}
	if runner.agent.HomeDir() != cfg.HomeDir {
		t.Fatal("SDK is using a different runtime home")
	}
}
