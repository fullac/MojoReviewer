// 在宿主准备阶段索引和召回项目记忆，使用独立预算，不消耗模型循环的超时窗口。
package agent

import (
	"context"
	"log/slog"
	"time"

	"github.com/mojoreviewer/mojoreviewer/internal/observe"
	"github.com/mojoreviewer/mojoreviewer/internal/projectmem"
)

func (r *Runner) PrepareMemory(ctx context.Context, input ReviewInput) projectmem.ContextBundle {
	if r == nil || r.memory == nil {
		return input.Memory
	}
	done := observe.Stage(ctx, "memory")
	defer done()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	indexed, err := r.memory.Ensure(ctx, input.Review.Repo, input.WorkDir, input.Review.HeadSHA)
	if err != nil {
		slog.Warn("项目记忆索引失败，继续审查", "repo", input.Review.Repo)
	} else {
		observe.From(ctx).Memory(indexed.CacheHit, indexed.Files)
	}
	var hints []string
	if input.Manifest != nil {
		for _, file := range input.Manifest.Files {
			hints = append(hints, file.Path)
			for _, symbol := range file.Symbols {
				hints = append(hints, symbol.Name)
			}
			if len(hints) >= 32 {
				break
			}
		}
	}
	return r.memory.LoadContext(ctx, input.Review.Repo, input.Review.Title, input.Review.Body, input.Review.Comment, hints...)
}
