package server

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mojoreviewer/mojoreviewer/internal/agent"
	"github.com/mojoreviewer/mojoreviewer/internal/review"
)

func reviewFailureComment(result agent.ReviewResult, err error) string {
	reason := "模型执行失败，审查未完成。"
	if errors.Is(err, context.DeadlineExceeded) {
		reason = "模型调用超时，审查未完成。"
	} else if errors.Is(err, context.Canceled) {
		reason = "任务被取消，审查未完成。"
	}
	var b strings.Builder
	b.WriteString("审查未完成（失败阶段：model），本次不是完整审查结论。\n\n")
	var failure *agent.ReviewFailure
	if errors.As(err, &failure) {
		b.WriteString(failure.Reason)
		if failure.TaskID != "" {
			fmt.Fprintf(&b, "\n运行时任务：%s。原运行时会话的恢复指令：`继续前台任务 %s`。", failure.TaskID, failure.TaskID)
		}
		if failure.Coverage != "" {
			b.WriteString("\n\n" + failure.Coverage)
		}
	} else {
		// SDK 错误可能带完整 prompt，不直接公开 err.Error()。
		b.WriteString(reason)
	}
	if result.Noted && strings.TrimSpace(result.Text) != "" {
		b.WriteString("\n\n中断前已记录的意见（覆盖不完整）：\n\n")
		b.WriteString(strings.TrimSpace(strings.TrimPrefix(result.Text, review.BotPrefix)))
	} else {
		b.WriteString("\n\n尚未形成可发布的审查意见；这不表示没有问题。")
	}
	return b.String()
}
