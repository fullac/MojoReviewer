package review

import (
	"fmt"
	"os/exec"
	"strings"
)

const maxDiffBytes = 120_000

// Diff 读取检出相对已固定的共同祖先的差异，不再访问远端。
func Diff(dir, baseSHA string) (string, error) {
	if strings.TrimSpace(baseSHA) == "" {
		return "", fmt.Errorf("缺少 base SHA")
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
	return text[:limit] + "\n\n…差异过长，已截断。请用 pr_file_diff 和 read_repo_file 查看具体文件。"
}
