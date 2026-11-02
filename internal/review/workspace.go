// 为每个仓库维护共享对象缓存，为每个 PR 创建独立 detached 工作目录与比较引用。
package review

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Workspace struct{ Dir string }

// Preparation remains serial even when callers outside the HTTP worker use it.
var prepareMu sync.Mutex
var commitID = regexp.MustCompile(`^(?:[a-fA-F0-9]{40}|[a-fA-F0-9]{64})$`)

const (
	baseRef      = "refs/worktree/review/base"
	mergeBaseRef = "refs/worktree/review/merge-base"
)

func Prepare(dataDir, repo, cloneURL, sha, sessionKey string) (Workspace, error) {
	return PrepareWithBase(dataDir, repo, cloneURL, sha, sessionKey, "", "")
}
func PrepareWithBase(dataDir, repo, cloneURL, sha, sessionKey, baseBranch, baseSHA string) (Workspace, error) {
	return PrepareWithBaseContext(context.Background(), dataDir, repo, cloneURL, sha, sessionKey, baseBranch, baseSHA)
}

func PrepareWithBaseContext(ctx context.Context, dataDir, repo, cloneURL, sha, sessionKey, baseBranch, baseSHA string) (Workspace, error) {
	prepareMu.Lock()
	defer prepareMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if strings.TrimSpace(repo) == "" || !commitID.MatchString(sha) || (baseSHA != "" && !commitID.MatchString(baseSHA)) {
		return Workspace{}, fmt.Errorf("仓库或固定提交无效")
	}
	if cloneURL == "" {
		cloneURL = "https://github.com/" + repo + ".git"
	}
	dataDir, err := filepath.Abs(dataDir)
	if err != nil {
		return Workspace{}, err
	}
	identity := strings.ToLower(repo) + "\x00" + cloneURL
	cache := filepath.Join(dataDir, "repos", workspaceID(repo, identity)+".git")
	dir := filepath.Join(dataDir, "workspaces", workspaceID(sessionKey, identity+"\x00"+sessionKey))
	// Existing legacy clone directories use another name and are left intact,
	// including unpublished fix work. Never delete them during migration.
	if _, err := os.Stat(filepath.Join(cache, "HEAD")); os.IsNotExist(err) {
		if err := os.MkdirAll(cache, 0700); err != nil {
			return Workspace{}, err
		}
		for _, args := range [][]string{{"init", "--bare"}, {"remote", "add", "origin", cloneURL}, {"config", "remote.origin.promisor", "true"}, {"config", "remote.origin.partialclonefilter", "blob:none"}} {
			if err := prepareGit(ctx, cache, args...); err != nil {
				return Workspace{}, err
			}
		}
	} else if err != nil {
		return Workspace{}, err
	}
	remote, err := gitOutput(cache, "remote", "get-url", "origin")
	if err != nil || strings.TrimSpace(remote) != cloneURL {
		return Workspace{}, fmt.Errorf("共享仓库来源不匹配")
	}
	existing := false
	if _, err := os.Lstat(dir); err == nil {
		common, err := gitOutput(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if err != nil {
			return Workspace{}, fmt.Errorf("已有工作目录不是受管理检出")
		}
		actual, _ := filepath.EvalSymlinks(strings.TrimSpace(common))
		expected, _ := filepath.EvalSymlinks(cache)
		if actual == "" || actual != expected {
			return Workspace{}, fmt.Errorf("工作目录不属于本仓库缓存")
		}
		existing = true
		for _, ref := range []string{mergeBaseRef, baseRef} {
			if err := prepareGit(ctx, dir, "update-ref", "-d", ref); err != nil {
				return Workspace{}, err
			}
		}
		status, err := gitOutput(dir, "status", "--porcelain")
		if err != nil || strings.TrimSpace(status) != "" {
			return Workspace{}, fmt.Errorf("工作目录包含未提交改动，已保留，请先处理")
		}
	} else if !os.IsNotExist(err) {
		return Workspace{}, err
	}
	args := []string{"fetch", "--no-tags", "--filter=blob:none", "origin", sha}
	if baseSHA != "" && baseSHA != sha {
		args = append(args, baseSHA)
	}
	if err := prepareGit(ctx, cache, args...); err != nil {
		return Workspace{}, err
	}
	if existing {
		if err := prepareGit(ctx, dir, "checkout", "--detach", sha); err != nil {
			return Workspace{}, err
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
			return Workspace{}, err
		}
		// A failed first checkout may leave a registration. Prune only entries
		// whose directory is gone; live/dirty working trees are never removed.
		if err := prepareGit(ctx, cache, "worktree", "prune"); err != nil {
			return Workspace{}, err
		}
		if err := prepareGit(ctx, cache, "worktree", "add", "--detach", dir, sha); err != nil {
			return Workspace{}, err
		}
	}
	if baseSHA != "" {
		if err := prepareGit(ctx, dir, "update-ref", baseRef, baseSHA); err != nil {
			return Workspace{}, err
		}
		ancestors, err := manifestGit(ctx, dir, 1024, "merge-base", "--all", baseSHA, sha)
		if err != nil || len(strings.Fields(ancestors)) != 1 {
			return Workspace{}, fmt.Errorf("无法确定唯一的 PR 共同祖先")
		}
		ancestor := strings.TrimSpace(ancestors)
		// Download blobs needed by the fixed diff now. Subsequent model tools must
		// not initiate a fetch through partial-clone lazy loading.
		cmd := exec.CommandContext(ctx, "git", "-C", dir, "diff", "--no-ext-diff", "--no-textconv", "--binary", ancestor, sha)
		cmd.Env = envWithAskpass()
		cmd.Stdout = io.Discard
		if err := cmd.Run(); err != nil {
			return Workspace{}, fmt.Errorf("准备差异对象失败")
		}
		if err := prepareGit(ctx, dir, "update-ref", mergeBaseRef, ancestor); err != nil {
			return Workspace{}, err
		}
	}
	return Workspace{Dir: dir}, nil
}

func workspaceID(label, identity string) string {
	sum := sha256.Sum256([]byte(identity))
	label = sanitize(label)
	if len(label) > 80 {
		label = label[:80]
	}
	return fmt.Sprintf("%s-%x", label, sum[:8])
}

func prepareGit(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = envWithAskpass()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git 准备失败: %w: %s", err, truncate(strings.TrimSpace(string(out)), 300))
	}
	return nil
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
