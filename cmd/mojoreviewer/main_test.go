package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestConfiguredLogLevelFiltersHostLogs(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	for _, level := range []string{"debug", "info", "warn", "error"} {
		t.Run(level, func(t *testing.T) {
			var out bytes.Buffer
			if err := configureLogging(level, &out); err != nil {
				t.Fatal(err)
			}
			slog.Debug("debug-message")
			slog.Info("info-message")
			slog.Warn("warn-message")
			slog.Error("error-message")
			for _, tc := range []struct {
				message string
				want    bool
			}{
				{"debug-message", level == "debug"},
				{"info-message", level == "debug" || level == "info"},
				{"warn-message", level != "error"},
				{"error-message", true},
			} {
				if strings.Contains(out.String(), tc.message) != tc.want {
					t.Fatalf("level=%s message=%s output=%s", level, tc.message, out.String())
				}
			}
		})
	}
}
