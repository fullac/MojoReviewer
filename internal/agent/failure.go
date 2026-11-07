package agent

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/mojoreviewer/mojoreviewer/internal/review"
)

var foregroundTaskID = regexp.MustCompile(`\btq-[0-9]+\b`)

var errRepeatedToolLoop = errors.New("repeated tool calls produced no new evidence")
var errEmptyModelResponse = errors.New("empty model response")

// The pinned SDK's ordinary loop returns nil error when its repeat guard stops
// execution. Convert that fixed runtime response to failure before publishing.
// Never retain its appended tool output, which may contain repository secrets.
func modelExecutionError(text string, err error) error {
	// 普通循环可能以空正文或固定的空回复文案返回 nil，宿主仍必须记为未完成。
	if err == nil && (strings.TrimSpace(text) == "" || strings.TrimSpace(text) == "I couldn't produce a complete answer this round. Please retry.") {
		return errEmptyModelResponse
	}
	if err == nil && strings.HasPrefix(strings.TrimSpace(text), "Detected repeated tool-call loop and stopped early to avoid timeout.\nLatest tool outputs:\n") {
		return errRepeatedToolLoop
	}
	return err
}

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
	if reason := upstreamReason(err); reason != "" {
		return reason
	}
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

func reviewResult(sessionID, text string, notes *review.Notes, progress *readProgress, err error) (ReviewResult, error) {
	err = modelExecutionError(text, err)
	result := ReviewResult{SessionID: sessionID, Notes: notes.Snapshot()}
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

// classifyReviewFailure 只输出固定归类，原始 SDK 文本不进入评论或宿主日志。
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
	if reason := upstreamReason(err); reason != "" {
		return reason
	}
	text := err.Error()
	switch {
	case errors.Is(err, errEmptyModelResponse), strings.Contains(text, "no new evidence after repeated attempts"), strings.Contains(text, "empty model response"), strings.Contains(text, "empty response"):
		return "模型空回复，审查未完成。请通过评论或管理台重新请求审查。"
	case strings.Contains(text, "repeated tool calls produced no new evidence"):
		return "重复读取未产生新证据，运行时已中断审查。"
	case strings.HasPrefix(text, "max iterations ("):
		return "模型执行达到迭代上限，审查未完成。"
	default:
		return "模型执行失败，审查未完成。"
	}
}
