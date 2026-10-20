package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/mojoreviewer/mojoreviewer/internal/agent"
	"github.com/mojoreviewer/mojoreviewer/internal/config"
	"github.com/mojoreviewer/mojoreviewer/internal/projectmem"
	"github.com/mojoreviewer/mojoreviewer/internal/server"
	"github.com/mojoreviewer/mojoreviewer/internal/store"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fatal(err)
	}
	if err := configureLogging(cfg.LogLevel, os.Stderr); err != nil {
		fatal(err)
	}
	if cfg.GitHub.Token != "" && os.Getenv("GH_TOKEN") == "" {
		_ = os.Setenv("GH_TOKEN", cfg.GitHub.Token)
	}
	runner, err := agent.New(cfg.Agent)
	if err != nil {
		fatal(err)
	}
	defer runner.Close()

	st, err := store.Open(filepath.Join(cfg.DataDir, "reviews.json"))
	if err != nil {
		fatal(err)
	}
	memStore, err := projectmem.Open(filepath.Join(cfg.DataDir, "project_memory.json"))
	if err != nil {
		fatal(err)
	}
	runner.AttachProjectMemory(memStore)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := server.New(cfg, runner, st).Start(ctx); err != nil {
		fatal(err)
	}
}

func configureLogging(level string, out io.Writer) error {
	var minimum slog.Level
	if err := minimum.UnmarshalText([]byte(level)); err != nil {
		return err
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{Level: minimum})))
	return nil
}

func fatal(err error) {
	slog.Error("MojoReviewer 启动失败", "error", err)
	os.Exit(1)
}
