package review

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewDiffsExcludeAdvancingBase(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	root := t.TempDir()
	seed := filepath.Join(root, "seed")
	origin := filepath.Join(root, "origin.git")
	run(t, "", "git", "init", "-b", "main", seed)
	run(t, seed, "git", "config", "user.name", "test")
	run(t, seed, "git", "config", "user.email", "test@example.com")
	run(t, seed, "mkdir", "-p", "basearea", "prarea")
	write(t, filepath.Join(seed, "basearea/base.go"), "package basearea\nfunc Base() int { return 1 }\n")
	write(t, filepath.Join(seed, "basearea/base_test.go"), "package basearea\n")
	write(t, filepath.Join(seed, "prarea/feature.go"), "package prarea\nfunc Feature() int { return 1 }\n")
	write(t, filepath.Join(seed, "prarea/feature_test.go"), "package prarea\n")
	run(t, seed, "git", "add", ".")
	run(t, seed, "git", "commit", "-m", "ancestor")
	ancestor := strings.TrimSpace(run(t, seed, "git", "rev-parse", "HEAD"))
	run(t, seed, "git", "switch", "-c", "feature")
	write(t, filepath.Join(seed, "prarea/feature.go"), "package prarea\nfunc Feature() int { return 2 }\n")
	run(t, seed, "git", "commit", "-am", "PR change")
	head := strings.TrimSpace(run(t, seed, "git", "rev-parse", "HEAD"))
	run(t, seed, "git", "switch", "main")
	write(t, filepath.Join(seed, "basearea/base.go"), "package basearea\nfunc Base() int { return 3 }\n")
	write(t, filepath.Join(seed, "base-only.txt"), "base addition\n")
	run(t, seed, "git", "add", ".")
	run(t, seed, "git", "commit", "-m", "base advances")
	base := strings.TrimSpace(run(t, seed, "git", "rev-parse", "HEAD"))
	run(t, "", "git", "clone", "--bare", seed, origin)
	ws, err := PrepareWithBase(root, "acme/web", "file://"+origin, head, "acme/web#7", "main", base)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(run(t, ws.Dir, "git", "merge-base", "refs/review/base", "HEAD")); got != ancestor {
		t.Fatalf("merge-base = %s, want %s", got, ancestor)
	}
	scope := ToolScope{Dir: ws.Dir}
	for _, tc := range []struct {
		name string
		call func() (string, error)
	}{
		{"prompt diff", func() (string, error) { return Diff(ws.Dir, base) }},
		{"stat", scope.Stat},
		{"file diff", func() (string, error) { return scope.FileDiff("prarea/feature.go") }},
		{"symbols", scope.ChangedSymbols},
		{"tests", func() (string, error) { return scope.TestsAround("") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := tc.call()
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, "prarea/feature") || strings.Contains(out, "basearea") || strings.Contains(out, "base-only") {
				t.Fatalf("应只包含 PR 自己的改动: %s", out)
			}
		})
	}
	if out, err := scope.FileDiff("basearea/base.go"); err != nil || !strings.Contains(out, "没有差异") {
		t.Fatalf("base 独有改动不应进入文件 diff: %s %v", out, err)
	}
	// 检出完成后，读取提示词 diff 不应再访问 remote。
	run(t, ws.Dir, "git", "remote", "set-url", "origin", "file:///nonexistent-mojo-diff-remote")
	if _, err := Diff(ws.Dir, base); err != nil {
		t.Fatalf("已准备的 diff 仍依赖额外 fetch: %v", err)
	}
	// 同一检出被复用时，PR rebase 后必须刷新共同祖先。
	run(t, seed, "git", "switch", "feature")
	run(t, seed, "git", "rebase", "main")
	rebasedHead := strings.TrimSpace(run(t, seed, "git", "rev-parse", "HEAD"))
	run(t, seed, "git", "push", origin, "HEAD:refs/heads/rebased-feature")
	run(t, ws.Dir, "git", "remote", "set-url", "origin", "file://"+origin)
	if _, err := PrepareWithBase(root, "acme/web", "file://"+origin, rebasedHead, "acme/web#7", "main", base); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(run(t, ws.Dir, "git", "rev-parse", mergeBaseRef)); got != base {
		t.Fatalf("复用检出保留了过期的共同祖先: %s, want %s", got, base)
	}
	if out, err := Diff(ws.Dir, base); err != nil || strings.Contains(out, "basearea") || !strings.Contains(out, "prarea/feature") {
		t.Fatalf("rebase 后的 diff 异常: %s %v", out, err)
	}
}

func TestPrepareRejectsUnrelatedHistories(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	root := t.TempDir()
	run(t, root, "git", "init", "-b", "main")
	run(t, root, "git", "config", "user.name", "test")
	run(t, root, "git", "config", "user.email", "test@example.com")
	run(t, root, "git", "commit", "--allow-empty", "-m", "base")
	base := strings.TrimSpace(run(t, root, "git", "rev-parse", "HEAD"))
	run(t, root, "git", "switch", "--orphan", "feature")
	run(t, root, "git", "commit", "--allow-empty", "-m", "unrelated head")
	head := strings.TrimSpace(run(t, root, "git", "rev-parse", "HEAD"))
	if _, err := PrepareWithBase(t.TempDir(), "acme/web", "file://"+root, head, "acme/web#7", "main", base); err == nil || !strings.Contains(err.Error(), "共同祖先") {
		t.Fatalf("不相关的历史不应生成误导性的 diff: %v", err)
	}
}
