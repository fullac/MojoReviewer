package review

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Workspace 是一个 PR 的本地只读检出。
type Workspace struct {
	Dir string
}

// Prepare 把 PR head 克隆到 dataDir/workspaces 下的稳定目录。
// 目录已存在时只 fetch 指定 SHA，不切换到别的仓库。
func Prepare(dataDir string, repo, cloneURL, sha, sessionKey string) (Workspace, error) {
	if strings.TrimSpace(repo) == "" || strings.TrimSpace(sha) == "" {
		return Workspace{}, fmt.Errorf("仓库或提交为空")
	}
	dir := filepath.Join(dataDir, "workspaces", sanitize(sessionKey))
	gitDir := filepath.Join(dir, ".git")
	if st, err := os.Stat(gitDir); err == nil && st.IsDir() {
		if err := git(dir, "fetch", "--depth", "1", "origin", sha); err != nil {
			return Workspace{}, err
		}
		if err := git(dir, "checkout", "--detach", "FETCH_HEAD"); err != nil {
			return Workspace{}, err
		}
		return Workspace{Dir: dir}, nil
	}
	if err := os.RemoveAll(dir); err != nil {
		return Workspace{}, err
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return Workspace{}, err
	}
	if cloneURL == "" {
		cloneURL = "https://github.com/" + repo + ".git"
	}
	cmd := exec.Command("git", "clone", "--depth", "1", cloneURL, dir)
	cmd.Env = envWithAskpass()
	if out, err := cmd.CombinedOutput(); err != nil {
		return Workspace{}, fmt.Errorf("git clone 失败: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if err := git(dir, "fetch", "--depth", "1", "origin", sha); err != nil {
		return Workspace{}, err
	}
	if err := git(dir, "checkout", "--detach", "FETCH_HEAD"); err != nil {
		return Workspace{}, err
	}
	return Workspace{Dir: dir}, nil
}

func git(dir string, args ...string) error {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = envWithAskpass()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s 失败: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func envWithAskpass() []string {
	env := os.Environ()
	token := os.Getenv("GH_TOKEN")
	if token == "" {
		token = os.Getenv("GITHUB_TOKEN")
	}
	if token == "" {
		return append(env, "GIT_TERMINAL_PROMPT=0")
	}
	dir, err := os.MkdirTemp("", "mojo-askpass")
	if err != nil {
		return append(env, "GIT_TERMINAL_PROMPT=0")
	}
	path := filepath.Join(dir, "askpass.sh")
	script := "#!/bin/sh\ncase \"$1\" in\n  *Username*) echo x-access-token ;;\n  *) echo \"$MOJO_GIT_TOKEN\" ;;\nesac\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		return append(env, "GIT_TERMINAL_PROMPT=0")
	}
	return append(env, "GIT_ASKPASS="+path, "GIT_TERMINAL_PROMPT=0", "MOJO_GIT_TOKEN="+token)
}

func sanitize(value string) string {
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "workspace"
	}
	return out
}
