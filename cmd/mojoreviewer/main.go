package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/mojoreviewer/mojoreviewer/internal/agent"
	"github.com/mojoreviewer/mojoreviewer/internal/config"
	"github.com/mojoreviewer/mojoreviewer/internal/server"
	"github.com/mojoreviewer/mojoreviewer/internal/store"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	if cfg.GitHub.Token != "" && os.Getenv("GH_TOKEN") == "" {
		_ = os.Setenv("GH_TOKEN", cfg.GitHub.Token)
	}
	runner, err := agent.New(cfg.Agent)
	if err != nil {
		log.Fatal(err)
	}
	defer runner.Close()

	st, err := store.Open(filepath.Join(cfg.DataDir, "reviews.json"))
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := server.New(cfg, runner, st).Start(ctx); err != nil {
		log.Fatal(err)
	}
}
