package agent

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/mojoreviewer/mojoreviewer/internal/review"
)

var foregroundTaskID = regexp.MustCompile(`\btq-[0-9]+\b`)
var foregroundTaskIDPattern = regexp.MustCompile(`^tq-[0-9]+$`)

// ReviewFailure 保留安全的中断原因和读取回执，原始 SDK 错误不进入公开评论。
type ReviewFailure struct {
	Reason   string
	TaskID   string
	Coverage string
	cause    error
}

// ModelFailureLog 返回可写入宿主日志的归类原因。
// 原始 SDK 错误可能含 prompt，不能进入日志字段。
func ModelFailureLog(err error) string {
	var failure *ReviewFailure
	if errors.As(err, &failure) {
		if failure.TaskID != "" {
			return failure.Reason + " task=" + failure.TaskID
		}
		return failure.Reason
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "模型调用超时，审查未完成。"
	}
	if errors.Is(err, context.Canceled) {
		return "任务被取消，审查未完成。"
	}
	return "模型执行失败，审查未完成。"
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
	reason := classifyReviewFailure(err)
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

// classifyReviewFailure maps a runtime error onto a public reason.
// Empty model replies and "no new evidence" are not the repeated-tool-read case,
// and the raw SDK text is never returned.
func classifyReviewFailure(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "模型调用超时，审查未完成。"
	}
	if errors.Is(err, context.Canceled) {
		return "任务被取消，审查未完成。"
	}
	text := err.Error()
	switch {
	case strings.Contains(text, "no new evidence after repeated attempts"),
		strings.Contains(text, "empty model response"),
		strings.Contains(text, "empty response"):
		return "模型空回复后被运行时挡住，审查未完成。这不是工具重复读取。"
	case strings.Contains(text, "repeated tool calls produced no new evidence"):
		return "重复读取未产生新证据，运行时已中断审查。"
	default:
		return "模型执行失败，审查未完成。"
	}
}
