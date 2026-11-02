// 验证串行分批上下文、总预算、笔记合并与失败范围保留。
package agent

import (
	"context"
	"errors"
	"github.com/mojoreviewer/mojoreviewer/internal/observe"
	"github.com/mojoreviewer/mojoreviewer/internal/review"
	"testing"
	"time"
)

func TestExecuteBatchesPreservesPartialResults(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ctx, m := observe.Start(ctx)
	batches := []review.Batch{{Files: []string{"a.go"}, Index: 1, Total: 3}, {Files: []string{"b.go"}, Index: 2, Total: 3}, {Files: []string{"c.go"}, Index: 3, Total: 3}}
	calls := 0
	n := review.Note{Severity: "建议", Path: "a.go", Line: 1, Body: "- **问题描述：** 问题。\n\n- **代码位置：** `a.go:1`\n\n- **修复建议：** 修复。"}
	result, err := executeBatches(ctx, ReviewInput{SessionID: "previous", Manifest: &review.Manifest{}}, batches, func(c context.Context, in ReviewInput) (ReviewResult, error) {
		calls++
		if in.SessionID != "" || in.Batch.Index != calls || !in.Manifest.Complete {
			t.Fatal(in)
		}
		deadline, ok := c.Deadline()
		if !ok || time.Until(deadline) > time.Second {
			t.Fatal("unbounded budget")
		}
		if calls == 2 {
			return ReviewResult{}, errors.New("failure")
		}
		return ReviewResult{Notes: []review.Note{n}}, nil
	})
	var failure *ReviewFailure
	if !errors.As(err, &failure) || calls != 3 || len(result.Notes) != 1 || len(result.Unreviewed) != 1 || result.Unreviewed[0] != "b.go" || m.Snapshot().BatchesCompleted != 2 {
		t.Fatalf("%+v %v", result, err)
	}
}
func TestExecuteBatchesStopsAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	calls := 0
	result, err := executeBatches(ctx, ReviewInput{}, []review.Batch{{Files: []string{"a"}}, {Files: []string{"b"}}}, func(context.Context, ReviewInput) (ReviewResult, error) {
		calls++
		cancel()
		return ReviewResult{}, context.Canceled
	})
	if err == nil || calls != 1 || len(result.Unreviewed) != 2 {
		t.Fatalf("%+v %v %d", result, err, calls)
	}
}

func TestExecuteBatchesPreservesDisabledManifest(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := executeBatches(ctx, ReviewInput{}, []review.Batch{{Files: []string{"a"}}}, func(_ context.Context, in ReviewInput) (ReviewResult, error) {
		if in.Manifest != nil {
			t.Fatal("batch reenabled disabled manifest")
		}
		return ReviewResult{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
