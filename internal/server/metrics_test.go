// 验证成功和失败尝试的阶段观测都会落盘，供管理台重启后查询。
package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/mojoreviewer/mojoreviewer/internal/agent"
	"github.com/mojoreviewer/mojoreviewer/internal/projectmem"
	"github.com/mojoreviewer/mojoreviewer/internal/store"
)

func TestAttemptMetricsPersistOnSuccessAndFailure(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[failed], func(t *testing.T) {
			s, r, _, _ := testServer(t, "", "")
			if failed {
				r.reviewResult = &agent.ReviewResult{SessionID: "test"}
				r.reviewErr = errors.New("private upstream error")
			}
			s.runOne(context.Background(), localReviewItem(t))
			raw, err := os.ReadFile(filepath.Join(s.cfg.DataDir, "activity.json"))
			if err != nil {
				t.Fatal(err)
			}
			var rows []store.Activity
			if err := json.Unmarshal(raw, &rows); err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1 || rows[0].Metrics == nil {
				t.Fatalf("missing metrics: %s", raw)
			}
			if failed && rows[0].State != "failed" {
				t.Fatalf("failure changed to success: %s", raw)
			}
			for _, phase := range []string{"checkout", "publish"} {
				if _, ok := rows[0].Metrics.Stages[phase]; !ok {
					t.Fatalf("missing phase %s: %s", phase, raw)
				}
			}
		})
	}
}

func TestHostPreparesMemoryForReviewAndFix(t *testing.T) {
	for _, fix := range []bool{false, true} {
		t.Run(map[bool]string{false: "review", true: "fix"}[fix], func(t *testing.T) {
			s, r, _, _ := testServer(t, "", "")
			r.memoryResult = projectmem.ContextBundle{Memories: []string{"prepared"}}
			item := localReviewItem(t)
			if fix {
				s.runFix(context.Background(), item, &store.Activity{})
			} else {
				s.runOne(context.Background(), item)
			}
			if len(r.memoryInputs) != 1 || r.memoryInputs[0].Manifest == nil {
				t.Fatal("host did not prepare memory with manifest")
			}
			inputs := r.inputs
			if fix {
				inputs = r.fixInputs
			}
			if len(inputs) != 1 || len(inputs[0].Memory.Memories) != 1 || inputs[0].Memory.Memories[0] != "prepared" {
				t.Fatal("prepared memory not passed to model")
			}
		})
	}
}
