// 验证观测记录的并发安全、体积上限与快照隔离，避免工具输入进入持久数据。
package observe

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBoundedSafeSnapshot(t *testing.T) {
	ctx, r := Start(context.Background())
	done := Stage(ctx, "checkout")
	var wg sync.WaitGroup
	for i := 0; i < 450; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); r.Tool("secret-not-a-tool", time.Millisecond, 20) }()
	}
	wg.Wait()
	done()
	done()
	r.Model(2, &Usage{Input: 10, Output: 2, Total: 12})
	r.Model(1, nil)
	s := r.Snapshot()
	if s.ToolCount != 450 || len(s.Tools) != 400 || s.Iterations != 3 || s.Usage.Total != 12 {
		t.Fatalf("bad snapshot: %+v", s)
	}
	raw, _ := json.Marshal(s)
	if strings.Contains(string(raw), "secret") {
		t.Fatal("unsafe tool name persisted")
	}
	s.Stages["bad"] = 1
	s.Usage.Total = 100
	s.Tools[0].Name = "mutated"
	next := r.Snapshot()
	if next.Stages["bad"] != 0 || next.Usage.Total != 12 || next.Tools[0].Name != "unknown" {
		t.Fatal("snapshot aliases recorder")
	}
	_, empty := Start(context.Background())
	if empty.Snapshot().Usage != nil {
		t.Fatal("missing usage must not become zero")
	}
}
