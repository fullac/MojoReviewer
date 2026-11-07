// 串行执行分批审查，在一个总预算内合并笔记并保留未完成范围。
package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mojoreviewer/mojoreviewer/internal/observe"
	"github.com/mojoreviewer/mojoreviewer/internal/review"
)

func (r *Runner) Review(ctx context.Context, input ReviewInput) (ReviewResult, error) {
	if r == nil || r.agent == nil {
		return ReviewResult{}, fmt.Errorf("agent 未启动")
	}
	timeout := r.timeout
	if timeout <= 0 {
		timeout = 20 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	batches, err := review.PlanBatches(ctx, input.WorkDir)
	if err != nil {
		return ReviewResult{}, &ReviewFailure{Reason: "审查批次准备失败，审查未完成。", cause: err}
	}
	if len(batches) == 1 || input.SessionID != "" {
		result, err := r.reviewOne(ctx, input)
		observe.From(ctx).Batches(1, boolInt(err == nil))
		if err == nil {
			for _, b := range batches {
				result.CoveredFiles = append(result.CoveredFiles, b.Files...)
			}
		} else {
			for _, b := range batches {
				result.Unreviewed = append(result.Unreviewed, b.Files...)
			}
		}
		return result, err
	}
	return executeBatches(ctx, input, batches, r.reviewOne)
}

func executeBatches(ctx context.Context, input ReviewInput, batches []review.Batch, run func(context.Context, ReviewInput) (ReviewResult, error)) (ReviewResult, error) {
	merged := &review.Notes{}
	result := ReviewResult{}
	seen := map[string]bool{}
	done := 0
	var firstErr error
	for i, b := range batches {
		if ctx.Err() != nil {
			for _, left := range batches[i:] {
				result.Unreviewed = append(result.Unreviewed, left.Files...)
			}
			if firstErr == nil {
				firstErr = ctx.Err()
			}
			break
		}
		next := input
		next.Batch = &b
		next.Manifest = review.BatchManifest(input.Manifest, b)
		// Every batch has its own context and read receipts; tools may read callers
		// outside the assigned files, but findings are still grounded in the PR.
		next.SessionID = ""
		deadline, _ := ctx.Deadline()
		budget := time.Until(deadline) / time.Duration(len(batches)-i)
		batchCtx, cancel := context.WithTimeout(ctx, budget)
		part, err := run(batchCtx, next)
		cancel()
		if part.SessionID != "" {
			result.SessionID = part.SessionID
		}
		result.Reads = append(result.Reads, part.Reads...)
		result.Rejected += part.Rejected
		for _, n := range part.Notes {
			key := fmt.Sprintf("%s:%d:%s:%s:%s:%s", n.Path, n.Line, n.Title, n.Body, n.FindingID, n.Status)
			if !seen[key] {
				seen[key] = true
				_ = merged.AddFinding(n)
			}
		}
		if err != nil {
			result.Unreviewed = append(result.Unreviewed, b.Files...)
			if firstErr == nil {
				firstErr = err
			}
			// 永久配置/权限错误对后续批次同样无效，避免重复请求同一上游。
			if permanentUpstreamFailure(err) {
				firstErr = err
				for _, left := range batches[i+1:] {
					result.Unreviewed = append(result.Unreviewed, left.Files...)
				}
				break
			}
		} else {
			done++
			result.CoveredFiles = append(result.CoveredFiles, b.Files...)
		}
	}
	observe.From(ctx).Batches(len(batches), done)
	result.Notes = merged.Snapshot()
	result.Noted = merged.Len() > 0
	result.Text = merged.Render()
	if result.Rejected > 0 {
		result.Text += fmt.Sprintf("\n\n宿主未发布 %d 条无法核验依据的意见。", result.Rejected)
	}
	if firstErr != nil {
		reason := "部分审查批次未完成。"
		var failure *ReviewFailure
		if errors.As(firstErr, &failure) {
			reason = failure.Reason
		}
		var coverage strings.Builder
		fmt.Fprintf(&coverage, "审查批次完成 %d/%d；未完成范围：", done, len(batches))
		for i, path := range result.Unreviewed {
			if i >= 50 || coverage.Len() > 6000 {
				fmt.Fprintf(&coverage, "\n其余 %d 个文件未展开。", len(result.Unreviewed)-i)
				break
			}
			fmt.Fprintf(&coverage, "\n- %q", path)
		}
		return result, &ReviewFailure{Reason: reason, Coverage: coverage.String(), cause: firstErr}
	}
	return result, nil
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
