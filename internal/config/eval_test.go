// 验证回放不要求 webhook 凭据，并覆盖生产目录与修复开关的隔离。
package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadEvalIsolatesRuntime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	os.WriteFile(path, []byte(`{"agent":{"provider":"openai-compatible","model":"test","home_dir":"/production","fix_enabled":true},"data_dir":"/production"}`), 0600)
	t.Setenv("MOJO_EVAL_API_KEY", "test")
	cfg, err := LoadEval(path, dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HomeDir != filepath.Join(dir, "luckyagent") || cfg.FixEnabled || cfg.FixPushEnabled || cfg.APIKey != "test" {
		t.Fatal("runtime isolation failed")
	}
}
