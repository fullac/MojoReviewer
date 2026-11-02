// 验证自动任务合并、执行前远端提交核对与原子回执失败时的队列保留。
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/mojoreviewer/mojoreviewer/internal/github"
	"github.com/mojoreviewer/mojoreviewer/internal/store"
	"os"
	"path/filepath"
	"testing"
)

func TestCoalescedAutomaticHeadUsesOneSlot(t *testing.T) {
	s, r, _, _ := testServer(t, "", "")
	for i := 0; i < 50; i++ {
		if w := autoRequest(s, fmt.Sprint(i)); w.Code != 202 {
			t.Fatal(w.Code)
		}
	}
	if len(s.jobs) != 1 || len(s.pending) != 1 {
		t.Fatal("queue grew", len(s.jobs), len(s.pending))
	}
	s.resolveHead = func(_ context.Context, item github.Review, _ string) (github.Review, error) {
		if item.HeadSHA != "49" {
			t.Fatal(item.HeadSHA)
		}
		item.HeadSHA = "newer-on-github"
		return item, nil
	}
	s.runOne(context.Background(), <-s.jobs)
	if len(r.inputs) != 0 || s.store.HasAutoHead("acme/web#7", "49") {
		t.Fatal("stale head ran or consumed reservation")
	}
	raw, _ := os.ReadFile(filepath.Join(s.cfg.DataDir, "activity.json"))
	var rows []store.Activity
	json.Unmarshal(raw, &rows)
	if len(rows) != 51 {
		t.Fatal(len(rows))
	}
	for _, a := range rows {
		if a.HeadSHA == "newer-on-github" {
			if a.State != "queued" {
				t.Fatal(a)
			}
			continue
		}
		if a.State != "skipped" || a.Stage != "superseded" {
			t.Fatal(a)
		}
	}
}
func TestCoalescingDoesNotReplaceExplicitComment(t *testing.T) {
	s, _, _, _ := testServer(t, "", "")
	explicit := queueComment(t, s)
	s.jobs <- explicit
	autoRequest(s, "A")
	autoRequest(s, "B")
	if len(s.jobs) != 2 {
		t.Fatal(len(s.jobs))
	}
	if got := <-s.jobs; got.Comment == "" {
		t.Fatal("comment replaced")
	}
}
func TestCoalescingReceiptFailureKeepsPrevious(t *testing.T) {
	s, _, _, _ := testServer(t, "", "")
	autoRequest(s, "A")
	path := filepath.Join(s.cfg.DataDir, "activity.json")
	os.Remove(path)
	os.Mkdir(path, 0700)
	if w := autoRequest(s, "B"); w.Code != 500 {
		t.Fatal(w.Code)
	}
	if s.queuedAuto["acme/web#7"].HeadSHA != "A" || !s.pending["acme/web#7:A"] || len(s.jobs) != 1 {
		t.Fatal("old task lost")
	}
}
