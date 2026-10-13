package review

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	defaultMaxChangedFiles = 40
	defaultMaxDiffBytes    = 200_000
)

// PublishLimits 是宿主提交/推送的安全上限。零值用默认。
type PublishLimits struct {
	MaxChangedFiles int
	MaxDiffBytes    int
	// PushEnabled false 时只检测改动，不 commit/push。
	PushEnabled bool
}

// PublishResult 是宿主侧提交并推送后的结果。
type PublishResult struct {
	Changed   bool
	Pushed    bool
	CommitSHA string
	Branch    string
	Files     []string
	Message   string
	Skipped   string // 非空表示因策略跳过
}

// PublishFix 把检出内的本地改动提交并推到 PR head 分支。
// 工作区是 detached checkout；这里会建/切到 head 分支再 push，不做 force。
func PublishFix(dir, headBranch, baseBranch, message string, limits PublishLimits) (PublishResult, error) {
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
	if bad := deniedPublishPaths(files); len(bad) > 0 {
		return PublishResult{Changed: true, Branch: headBranch, Files: files},
			fmt.Errorf("拒绝提交敏感路径: %s", strings.Join(bad, ", "))
	}
	maxFiles := limits.MaxChangedFiles
	if maxFiles <= 0 {
		maxFiles = defaultMaxChangedFiles
	}
	if len(files) > maxFiles {
		return PublishResult{Changed: true, Branch: headBranch, Files: files},
			fmt.Errorf("改动文件数 %d 超过上限 %d", len(files), maxFiles)
	}
	maxDiff := limits.MaxDiffBytes
	if maxDiff <= 0 {
		maxDiff = defaultMaxDiffBytes
	}
	diff, err := gitOutput(dir, "diff", "--binary", "HEAD")
	if err != nil {
		// untracked 不在 diff HEAD；再用 --stat 近似不够，改用 diff --cached after add 前用 status 大小
		diff = status
	}
	// 未暂存时 diff HEAD 不含 untracked；用 status + diff 合计粗检
	unstaged, _ := gitOutput(dir, "diff", "--binary")
	untrackedSize := 0
	for _, f := range files {
		// 粗估：读 porcelain 已有；对 untracked 用文件尺寸
		p := filepath.Join(dir, f)
		if fi, err := fileSize(p); err == nil {
			untrackedSize += fi
		}
	}
	total := len(diff) + len(unstaged)
	if untrackedSize > total {
		total = untrackedSize
	}
	if total > maxDiff {
		return PublishResult{Changed: true, Branch: headBranch, Files: files},
			fmt.Errorf("改动约 %d 字节，超过上限 %d", total, maxDiff)
	}

	if !limits.PushEnabled {
		return PublishResult{
			Changed: true,
			Branch:  headBranch,
			Files:   files,
			Skipped: "fix_push_enabled=false，已保留工作区改动，未 commit/push",
		}, nil
	}

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
	// add 后再拦一次 index 里的敏感路径
	cached, _ := gitOutput(dir, "diff", "--cached", "--name-only")
	cachedFiles := splitNonEmpty(cached)
	if bad := deniedPublishPaths(cachedFiles); len(bad) > 0 {
		_ = git(dir, "reset", "HEAD")
		return PublishResult{Changed: true, Branch: headBranch, Files: cachedFiles},
			fmt.Errorf("拒绝提交敏感路径: %s", strings.Join(bad, ", "))
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
		Pushed:    true,
		CommitSHA: sha,
		Branch:    headBranch,
		Files:     files,
		Message:   msg,
	}, nil
}

func fileSize(path string) (int, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	if info.IsDir() {
		return 0, nil
	}
	return int(info.Size()), nil
}

func splitNonEmpty(raw string) []string {
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// deniedPublishPaths 返回不允许随 fix 提交的路径。
func deniedPublishPaths(files []string) []string {
	var bad []string
	for _, f := range files {
		if publishPathDenied(f) {
			bad = append(bad, f)
		}
	}
	return bad
}

func publishPathDenied(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	// 统一用 /
	p := filepath.ToSlash(path)
	base := strings.ToLower(filepath.Base(p))
	lower := strings.ToLower(p)

	// 路径段
	for _, seg := range strings.Split(lower, "/") {
		switch seg {
		case ".git", ".ssh", ".gnupg", "node_modules", "vendor":
			return true
		}
	}
	// 文件名模式
	denyNames := []string{
		".env", ".env.local", ".env.production", ".env.development",
		"credentials.json", "credentials.xml", "secrets.json", "secret.json",
		"id_rsa", "id_ed25519", "id_ecdsa", "authorized_keys", "known_hosts",
		"*.pem", // handled below
	}
	for _, n := range denyNames {
		if base == n {
			return true
		}
	}
	switch {
	case strings.HasSuffix(base, ".pem"),
		strings.HasSuffix(base, ".key"),
		strings.HasSuffix(base, ".p12"),
		strings.HasSuffix(base, ".pfx"),
		strings.HasSuffix(base, ".jks"),
		strings.HasSuffix(base, ".keystore"),
		strings.HasSuffix(base, ".kdbx"),
		base == "token",
		base == "api_key",
		strings.HasPrefix(base, ".env."):
		return true
	}
	// 常见密钥文件
	if strings.Contains(lower, "service-account") && strings.HasSuffix(base, ".json") {
		return true
	}
	if strings.Contains(base, "secret") && (strings.HasSuffix(base, ".json") || strings.HasSuffix(base, ".yml") || strings.HasSuffix(base, ".yaml") || strings.HasSuffix(base, ".toml")) {
		return true
	}
	return false
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
		if line == "" {
			continue
		}
		// porcelain v1: XY SPACE path；rename: XY SPACE old -> new
		path := line
		if len(line) >= 3 {
			path = strings.TrimSpace(line[3:])
		} else {
			path = strings.TrimSpace(line)
		}
		if i := strings.Index(path, " -> "); i >= 0 {
			path = path[i+4:]
		}
		// quoted paths
		path = strings.Trim(path, "\"")
		if path != "" {
			out = append(out, path)
		}
	}
	return out
}

func truncateCommitMessage(msg string) string {
	msg = strings.ReplaceAll(msg, "\r\n", "\n")
	msg = strings.TrimSpace(msg)
	runes := []rune(msg)
	if len(runes) > 200 {
		msg = string(runes[:200]) + "…"
	}
	if !strings.HasPrefix(msg, "MojoReviewer") {
		msg = "MojoReviewer fix: " + msg
	}
	return msg
}

// Render 给 PR 评论用。
func (p PublishResult) Render() string {
	if p.Skipped != "" {
		var b strings.Builder
		fmt.Fprintf(&b, "%s\n", p.Skipped)
		if len(p.Files) > 0 {
			b.WriteString("- pending files:\n")
			for _, f := range p.Files {
				fmt.Fprintf(&b, "  - `%s`\n", f)
			}
		}
		return strings.TrimSpace(b.String())
	}
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
