// 为离线回放读取模型配置，并强制使用隔离目录、关闭修复与发布配置。
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

func LoadEval(path, home string) (AgentConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return AgentConfig{}, err
	}
	var file fileConfig
	if err := json.Unmarshal(raw, &file); err != nil {
		return AgentConfig{}, err
	}
	file.DataDir = home
	file.Agent.HomeDir = filepath.Join(home, "luckyagent")
	file.Agent.FixEnabled = false
	disabled := false
	file.Agent.FixPushEnabled = &disabled
	file.GitHub.Token = ""
	file.GitHub.WebhookSecret = "evaluation-unused"
	if key := os.Getenv("MOJO_EVAL_API_KEY"); key != "" {
		file.Agent.APIKey = key
	}
	cfg, err := normalize(file)
	return cfg.Agent, err
}
