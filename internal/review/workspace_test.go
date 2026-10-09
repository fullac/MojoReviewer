package review

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareChecksOutHeadSHA(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("没有 git")
	}
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	seed := filepath.Join(root, "seed")
	run(t, "", "git", "init", "--bare", origin)
	run(t, "", "git", "clone", origin, seed)
	run(t, seed, "git", "config", "user.email", "mojo@example.com")
	run(t, seed, "git", "config", "user.name", "mojo")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, seed, "git", "add", "README.md")
	run(t, seed, "git", "commit", "-m", "init")
	base := strings.TrimSpace(run(t, seed, "git", "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, seed, "git", "commit", "-am", "change")
	head := strings.TrimSpace(run(t, seed, "git", "rev-parse", "HEAD"))
	run(t, seed, "git", "push", "origin", "HEAD:refs/heads/main")

	ws, err := PrepareWithBase(root, "acme/web", origin, head, "acme/web#1", "main", base)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(run(t, ws.Dir, "git", "rev-parse", "HEAD"))
	if got != head {
		t.Fatalf("检出 %s，期望 %s", got, head)
	}
	baseRef := strings.TrimSpace(run(t, ws.Dir, "git", "rev-parse", "refs/review/base"))
	if baseRef != base {
		t.Fatalf("base ref 指向 %s，期望 %s", baseRef, base)
	}
	originBase := strings.TrimSpace(run(t, ws.Dir, "git", "rev-parse", "refs/remotes/origin/main"))
	if originBase != base {
		t.Fatalf("origin/main 指向 %s，期望 %s", originBase, base)
	}
	mergeBase := strings.TrimSpace(run(t, ws.Dir, "git", "merge-base", "refs/review/base", "HEAD"))
	if mergeBase != base {
		t.Fatalf("merge-base 为 %s，期望 %s", mergeBase, base)
	}
	diff, err := Diff(ws.Dir, base)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "v2") {
		t.Fatalf("diff 未包含变更: %s", diff)
	}
}

func run(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v: %s", name, args, err, out)
	}
	return string(out)
}
