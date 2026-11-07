package server

import (
	"log/slog"

	"github.com/mojoreviewer/mojoreviewer/internal/agent"
	"github.com/mojoreviewer/mojoreviewer/internal/store"
)

func (s *Server) progress(id string, event store.ProgressEvent) {
	if id == "" {
		return
	}
	if err := store.AppendProgress(s.cfg.DataDir, id, event); err != nil {
		slog.Warn("记录审查进度失败", "error", err)
	}
}

func (s *Server) failProgress(id, stage string, err error) {
	s.progress(id, store.ProgressEvent{Kind: "fail", Summary: stage + "：" + publicFailure(err)})
}

func modelProgress(err error) string {
	if err == nil {
		return ""
	}
	return agent.ModelFailureLog(err)
}
