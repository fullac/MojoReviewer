package review

import (
	"fmt"
	"os/exec"
	"strings"
)

const maxDiffBytes = 16_000

// Diff 读取当前检出相对已固定共同祖先的代码差异（调试/测试用）。
// 调用方须先用同一 baseSHA 完成 PrepareWithBase；这里不再 fetch 或改动基线。
// 审查主路径不再把返回值写入 prompt；模型应通过 pr_diff_stat / pr_file_diff 按需读取（issue #21）。
// 失败时返回说明，不让调用方因 diff 命令中断。
func Diff(dir, baseSHA string) (string, error) {
	if strings.TrimSpace(baseSHA) == "" {
		return "", fmt.Errorf("缺少 base SHA")
	}
	pinnedBase, err := gitOutput(dir, "rev-parse", "--verify", baseRef)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(pinnedBase) != strings.TrimSpace(baseSHA) {
		return "", fmt.Errorf("base SHA 与已准备的检出不一致")
	}
	cmd := exec.Command("git", "-C", dir, "diff", "--stat", "--patch", mergeBaseRef, "HEAD")
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		return "", fmt.Errorf("git diff 失败: %w: %s", err, truncate(text, 500))
	}
	if text == "" {
		return "（没有代码差异）", nil
	}
	return truncate(text, maxDiffBytes), nil
}

func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "\n\n…差异过长，已截断。请用 pr_diff_stat / pr_file_diff 按文件查看。"
}
