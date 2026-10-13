package review

import (
	"fmt"
	"os/exec"
	"strings"
)

// PublishResult 是宿主侧提交并推送后的结果。
type PublishResult struct {
	Changed   bool
	CommitSHA string
	Branch    string
	Files     []string
	Message   string
}

// PublishFix 把检出内的本地改动提交并推到 PR head 分支。
// 工作区是 detached checkout；这里会建/切到 head 分支再 push，不做 force。
func PublishFix(dir, headBranch, baseBranch, message string) (PublishResult, error) {
	dir = strings.TrimSpace(dir)
	headBranch = strings.TrimSpace(headBranch)
	baseBranch = strings.TrimSpace(baseBranch)
	if dir == "" {
		return PublishResult{}, fmt.Errorf("工作目录为空")
	}
	if headBranch == "" {
		return PublishResult{}, fmt.Errorf("缺少 PR head 分支，无法推送")
	}
	if baseBranch != "" && headBranch == baseBranch {
		return PublishResult{}, fmt.Errorf("拒绝推送到 base 分支 %s", baseBranch)
	}
	if err := checkRefFormat(headBranch); err != nil {
		return PublishResult{}, err
	}

	status, err := gitOutput(dir, "status", "--porcelain")
	if err != nil {
		return PublishResult{}, err
	}
	status = strings.TrimSpace(status)
	if status == "" {
		return PublishResult{Changed: false, Branch: headBranch, Message: "工作区没有可提交的改动"}, nil
	}
	files := changedFiles(status)
	msg := strings.TrimSpace(message)
	if msg == "" {
		msg = "MojoReviewer fix"
	}
	msg = truncateCommitMessage(msg)

	// Detached HEAD → 本地分支，再推到 origin 的 head 分支。
	if err := git(dir, "switch", "-C", headBranch); err != nil {
		return PublishResult{}, fmt.Errorf("切换到 head 分支失败: %w", err)
	}
	if err := git(dir, "add", "-A"); err != nil {
		return PublishResult{}, fmt.Errorf("暂存改动失败: %w", err)
	}
	cmd := exec.Command("git", "-C", dir,
		"-c", "user.name=MojoReviewer",
		"-c", "user.email=mojoreviewer@users.noreply.github.com",
		"commit", "-m", msg,
	)
	cmd.Env = envWithAskpass()
	if out, err := cmd.CombinedOutput(); err != nil {
		return PublishResult{}, fmt.Errorf("git commit 失败: %w: %s", err, strings.TrimSpace(string(out)))
	}
	sha, err := gitOutput(dir, "rev-parse", "HEAD")
	if err != nil {
		return PublishResult{}, err
	}
	sha = strings.TrimSpace(sha)
	refspec := "HEAD:refs/heads/" + headBranch
	if err := git(dir, "push", "origin", refspec); err != nil {
		return PublishResult{Changed: true, CommitSHA: sha, Branch: headBranch, Files: files, Message: msg},
			fmt.Errorf("git push 失败: %w", err)
	}
	return PublishResult{
		Changed:   true,
		CommitSHA: sha,
		Branch:    headBranch,
		Files:     files,
		Message:   msg,
	}, nil
}

func checkRefFormat(branch string) error {
	cmd := exec.Command("git", "check-ref-format", "--branch", branch)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("head 分支无效: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func changedFiles(status string) []string {
	lines := strings.Split(status, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// porcelain: XY <path> 或 rename
		path := line
		if len(line) > 3 {
			path = strings.TrimSpace(line[3:])
		}
		if i := strings.Index(path, " -> "); i >= 0 {
			path = path[i+4:]
		}
		if path != "" {
			out = append(out, path)
		}
	}
	return out
}

func truncateCommitMessage(msg string) string {
	msg = strings.ReplaceAll(msg, "\r\n", "\n")
	msg = strings.TrimSpace(msg)
	if len(msg) > 72 {
		// 首行限制，其余可保留在正文；这里整段截断够用
		runes := []rune(msg)
		if len(runes) > 200 {
			msg = string(runes[:200]) + "…"
		}
	}
	if !strings.HasPrefix(msg, "MojoReviewer") {
		msg = "MojoReviewer fix: " + msg
	}
	return msg
}

// Render 给 PR 评论用。
func (p PublishResult) Render() string {
	if !p.Changed {
		return "未检测到工作区改动，没有 commit / push。"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "已提交并推送到 `%s`\n", p.Branch)
	fmt.Fprintf(&b, "- commit: `%s`\n", shortSHA(p.CommitSHA))
	fmt.Fprintf(&b, "- message: %s\n", p.Message)
	if len(p.Files) > 0 {
		b.WriteString("- files:\n")
		for _, f := range p.Files {
			fmt.Fprintf(&b, "  - `%s`\n", f)
		}
	}
	return strings.TrimSpace(b.String())
}

func shortSHA(sha string) string {
	sha = strings.TrimSpace(sha)
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
