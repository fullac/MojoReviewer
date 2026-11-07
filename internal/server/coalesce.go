// 处理乱序 webhook：过期任务跳过后，为 GitHub 当前 head 保留一个有界自动任务。
package server

import (
	"fmt"
	"github.com/mojoreviewer/mojoreviewer/internal/github"
	"github.com/mojoreviewer/mojoreviewer/internal/store"
	"log/slog"
	"time"
)

func (s *Server) queueCurrentHead(item github.Review) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := item.SessionKey + ":" + item.HeadSHA
	if s.pending[key] || s.store.HasAutoHead(item.SessionKey, item.HeadSHA) {
		return
	}
	// An already queued event will recheck the remote itself. Do not add a
	// second slot for the same PR even when deliveries arrive out of order.
	if _, ok := s.queuedAuto[item.SessionKey]; ok {
		return
	}
	if len(s.jobs) == cap(s.jobs) {
		slog.Warn("最新提交补排跳过，队列已满", "pr", item.SessionKey)
		return
	}
	item.AttemptID = fmt.Sprintf("current:%d", time.Now().UnixNano())
	if err := s.store.Activity(store.Activity{ID: item.AttemptID, Repo: item.Repo, Number: item.Number, HeadSHA: item.HeadSHA, State: "queued"}); err != nil {
		slog.Error("最新提交补排回执失败", "pr", item.SessionKey)
		return
	}
	s.pending[key] = true
	s.queuedAuto[item.SessionKey] = item
	s.jobs <- item
}
