package review

import (
	"fmt"
	"os/exec"
	"strings"
)

const maxDiffBytes = 120_000

// Diff 读取当前检出相对 base 的代码差异。失败时返回说明，不让审查因 diff 命令中断。
func Diff(dir, baseSHA string) (string, error) {
	if strings.TrimSpace(baseSHA) == "" {
		return "", fmt.Errorf("缺少 base SHA")
	}
	if err := git(dir, "fetch", "--depth", "1", "origin", baseSHA); err != nil {
		return "", err
	}
	cmd := exec.Command("git", "-C", dir, "diff", "--stat", "--patch", baseSHA, "HEAD")
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
	return text[:limit] + "\n\n…差异过长，已截断。请在仓库目录内用 file_read 查看具体文件。"
}
