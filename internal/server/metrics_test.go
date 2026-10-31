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
