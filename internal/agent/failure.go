package agent

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/mojoreviewer/mojoreviewer/internal/review"
)

var foregroundTaskID = regexp.MustCompile(`\btq-[0-9]+\b`)

// ReviewFailure 保留安全的中断原因和读取回执，原始 SDK 错误不进入公开评论。
type ReviewFailure struct {
	Reason   string
	TaskID   string
	Coverage string
	cause    error
}

func (f *ReviewFailure) Error() string { return f.Reason }
func (f *ReviewFailure) Unwrap() error { return f.cause }

func reviewResult(sessionID, _ string, notes *review.Notes, progress *readProgress, err error) (ReviewResult, error) {
	result := ReviewResult{SessionID: sessionID}
	if notes.Len() > 0 {
		result.Text = notes.Render()
		result.Noted = true
	}
	if err == nil {
		if !result.Noted {
			result.Text = notes.Render()
		}
		return result, nil
	}
	reason := "模型执行失败，审查未完成。"
	if strings.Contains(err.Error(), "repeated tool calls produced no new evidence") {
		reason = "重复读取未产生新证据，运行时已中断审查。"
	} else if errors.Is(err, context.DeadlineExceeded) {
		reason = "模型调用超时，审查未完成。"
	} else if errors.Is(err, context.Canceled) {
		reason = "任务被取消，审查未完成。"
	}
	failure := &ReviewFailure{Reason: reason, Coverage: progress.summary(), cause: err}
	// 只从运行时状态行提取 ID，不从可能包含 PR 文本的 title 中提取。
	for _, line := range strings.Split(err.Error(), "\n") {
		if strings.Contains(line, "[blocked]") || strings.Contains(line, "[failed]") {
			failure.TaskID = foregroundTaskID.FindString(line)
			if failure.TaskID != "" {
				break
			}
		}
	}
	return result, failure
}
