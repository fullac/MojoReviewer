package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ConfigPath 是相对当前工作目录的配置文件。
const ConfigPath = ".MojoReviewer/config.json"

// Config 是进程配置，来自 .MojoReviewer/config.json。
type Config struct {
	LogLevel string
	Server   ServerConfig
	GitHub   GitHubConfig
	Agent    AgentConfig
	DataDir  string
}

// ServerConfig 是本机 HTTP 监听配置。
type ServerConfig struct {
	Host string
	Port int
}

// Addr 返回 host:port。
func (s ServerConfig) Addr() string {
	host := s.Host
	if host == "" {
		host = "127.0.0.1"
	}
	port := s.Port
	if port == 0 {
		port = 9968
	}
	return fmt.Sprintf("%s:%d", host, port)
}

// GitHubConfig 是 webhook、访问令牌和仓库范围。
// Host 是 gh 访问的 GitHub 主机名。空值表示沿用 gh 自己的认证上下文，
// 这样 GitHub Enterprise Server 或非 github.com 的默认主机都能工作。
type GitHubConfig struct {
	Host          string
	WebhookSecret string
	Token         string
	Repos         []string
	TriggerUser   string
}

// AgentConfig 是嵌入的 LuckyAgent。一个进程只用一个模型。
type AgentConfig struct {
	Provider    string
	Model       string
	APIKey      string
	APIBase     string
	TaskTimeout time.Duration
	HomeDir     string
}

type fileConfig struct {
	LogLevel string `json:"log_level"`
	DataDir  string `json:"data_dir"`
	Server   struct {
		Host string `json:"host"`
		Port int    `json:"port"`
	} `json:"server"`
	GitHub struct {
		Host          string   `json:"host"`
		WebhookSecret string   `json:"webhook_secret"`
		Token         string   `json:"token"`
		Repos         []string `json:"repos"`
		TriggerUser   string   `json:"trigger_user"`
	} `json:"github"`
	Agent struct {
		Provider    string `json:"provider"`
		Model       string `json:"model"`
		APIKey      string `json:"api_key"`
		APIBase     string `json:"api_base"`
		TaskTimeout string `json:"task_timeout"`
		HomeDir     string `json:"home_dir"`
	} `json:"agent"`
}

// Load 读取当前目录下的 .MojoReviewer/config.json。
func Load() (Config, error) {
	raw, err := os.ReadFile(ConfigPath)
	if err != nil {
		return Config{}, fmt.Errorf("读取 %s: %w", ConfigPath, err)
	}
	var file fileConfig
	if err := json.Unmarshal(raw, &file); err != nil {
		return Config{}, fmt.Errorf("解析 %s: %w", ConfigPath, err)
	}
	return normalize(file)
}

func normalize(file fileConfig) (Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	dataDir := strings.TrimSpace(file.DataDir)
	if dataDir == "" {
		dataDir = filepath.Join(home, ".mojoreviewer")
	}
	timeout := 20 * time.Minute
	if raw := strings.TrimSpace(file.Agent.TaskTimeout); raw != "" {
		timeout, err = time.ParseDuration(raw)
		if err != nil || timeout <= 0 {
			return Config{}, fmt.Errorf("agent.task_timeout 无效: %s", raw)
		}
	}
	agentHome := strings.TrimSpace(file.Agent.HomeDir)
	if agentHome == "" {
		agentHome = filepath.Join(dataDir, "luckyagent")
	}
	cfg := Config{
		LogLevel: fallback(file.LogLevel, "info"),
		DataDir:  dataDir,
		Server: ServerConfig{
			Host: fallback(file.Server.Host, "127.0.0.1"),
			Port: file.Server.Port,
		},
		GitHub: GitHubConfig{
			Host:          githubHost(file.GitHub.Host),
			WebhookSecret: strings.TrimSpace(file.GitHub.WebhookSecret),
			Token:         strings.TrimSpace(file.GitHub.Token),
			Repos:         compact(file.GitHub.Repos),
			TriggerUser:   strings.TrimPrefix(strings.TrimSpace(file.GitHub.TriggerUser), "@"),
		},
		Agent: AgentConfig{
			Provider:    fallback(file.Agent.Provider, "openai"),
			Model:       fallback(file.Agent.Model, "gpt-5.4-mini"),
			APIKey:      strings.TrimSpace(file.Agent.APIKey),
			APIBase:     strings.TrimSpace(file.Agent.APIBase),
			TaskTimeout: timeout,
			HomeDir:     agentHome,
		},
	}
	if cfg.Server.Port == 0 {
		cfg.Server.Port = 9968
	}
	if cfg.Agent.APIKey == "" {
		return Config{}, fmt.Errorf("%s 缺少 agent.api_key", ConfigPath)
	}
	if cfg.GitHub.WebhookSecret == "" {
		return Config{}, fmt.Errorf("%s 缺少 github.webhook_secret", ConfigPath)
	}
	return cfg, nil
}

// githubHost 只接受主机名。带协议、路径、端口或空白的值都视为无效并忽略，
// 让 gh 继续使用它已经配置好的主机，而不是把错误主机写进请求。
func githubHost(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, " \t/\\:@") {
		return ""
	}
	return value
}

func fallback(value, def string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return def
	}
	return value
}

func compact(items []string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}
