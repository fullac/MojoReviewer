// 验证共享对象缓存、工作目录引用隔离、离线工具读取和未提交修复保护。
package review

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSharedCacheKeepsPerPRBaselines(t *testing.T) {
	origin, ancestor, base, head := divergedReviewRepo(t)
	root := t.TempDir()
	a, err := PrepareWithBase(root, "acme/web", origin, head, "pr1", "trunk", base)
	if err != nil {
		t.Fatal(err)
	}
	b, err := PrepareWithBase(root, "acme/web", origin, base, "pr2", "trunk", base)
	if err != nil {
		t.Fatal(err)
	}
	common := func(dir string) string {
		return strings.TrimSpace(run(t, dir, "git", "rev-parse", "--path-format=absolute", "--git-common-dir"))
	}
	if common(a.Dir) != common(b.Dir) || a.Dir == b.Dir {
		t.Fatal("cache not shared or checkout not isolated")
	}
	for dir, want := range map[string]string{a.Dir: ancestor, b.Dir: base} {
		if got := strings.TrimSpace(run(t, dir, "git", "rev-parse", mergeBaseRef)); got != want {
			t.Fatal("baseline overwritten", got, want)
		}
	}
	if st, err := os.Stat(filepath.Join(a.Dir, ".git")); err != nil || st.IsDir() {
		t.Fatal("expected linked worktree")
	}
	run(t, a.Dir, "git", "remote", "set-url", "origin", filepath.Join(root, "offline"))
	if out, err := (ToolScope{Dir: a.Dir}).FileDiff("feature/calc.go"); err != nil || !strings.Contains(out, "return 2") {
		t.Fatal("tool required remote", err)
	}
}
func TestPreparePreservesDirtyFixAndInvalidatesBaseline(t *testing.T) {
	origin, _, base, head := divergedReviewRepo(t)
	root := t.TempDir()
	ws, err := PrepareWithBase(root, "acme/web", origin, head, "pr", "trunk", base)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(ws.Dir, "unpublished.txt")
	write(t, path, "keep me")
	if _, err := PrepareWithBase(root, "acme/web", origin, head, "pr", "trunk", base); err == nil {
		t.Fatal("dirty worktree overwritten")
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != "keep me" {
		t.Fatal("fix lost")
	}
	if _, err := gitOutput(ws.Dir, "rev-parse", "--verify", mergeBaseRef); err == nil {
		t.Fatal("stale baseline retained")
	}
}
func TestWorkspaceNamesDoNotCollide(t *testing.T) {
	if workspaceID("a/b#1", "one") == workspaceID("a-b-1", "two") {
		t.Fatal("collision")
	}
}
