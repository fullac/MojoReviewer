package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mojoreviewer/mojoreviewer/internal/config"
)

const modelTemperature = 0.5

// The SDK exposes no request timeout option. Generate only this application's
// isolated bootstrap settings, then let sdk.New apply its public overrides.
// Canonical models fields keep chat and compaction on the configured model.
func writeRuntimeConfig(cfg config.AgentConfig) error {
	if strings.TrimSpace(cfg.HomeDir) == "" {
		return fmt.Errorf("LuckyAgent HomeDir 不能为空")
	}
	requestTimeout := cfg.RequestTimeout
	if requestTimeout <= 0 {
		requestTimeout = 5 * time.Minute
	}
	seconds := int((requestTimeout + time.Second - 1) / time.Second)
	tokens := cfg.MaxContextTokens
	if tokens <= 0 {
		tokens = 40000
	}
	endpoint := map[string]any{"provider": cfg.Provider, "api_key": cfg.APIKey, "api_base": cfg.APIBase}
	raw, err := json.Marshal(map[string]any{
		"temperature": modelTemperature,
		"models":      map[string]any{"active": map[string]string{"chat": cfg.Model, "compact": cfg.Model}, "endpoints": map[string]any{"chat": endpoint, "compact": endpoint}},
		"context":     map[string]int{"max_context_tokens": tokens},
		// Mojo owns task deadlines and publication. The SDK's foreground completion
		// verifier receives no acceptance criteria for these embedded chat turns.
		"agent": map[string]any{
			"foreground":              map[string]bool{"enabled": false},
			"max_iterations":          30,
			"timeout_seconds":         seconds,
			"simple_local_inspection": map[string]int{"timeout_seconds": seconds},
		},
		"limits": map[string]any{"timeout_seconds": seconds, "temperature": modelTemperature},
	})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.HomeDir, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(cfg.HomeDir, ".runtime-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(cfg.HomeDir, "config.json"))
}
