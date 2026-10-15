package review

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublishFixCommitsAndPushes(t *testing.T) {
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
	run(t, seed, "git", "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, seed, "git", "commit", "-am", "head")
	head := strings.TrimSpace(run(t, seed, "git", "rev-parse", "HEAD"))
	run(t, seed, "git", "push", "-u", "origin", "feature")
	run(t, seed, "git", "push", "origin", "HEAD:refs/heads/main")

	ws, err := PrepareWithBase(root, "acme/web", origin, head, "acme/web#9", "main", base)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.Dir, "README.md"), []byte("v3-fixed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.Dir, "fix.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := PublishFix(ws.Dir, "feature", "main", "改 README", PublishLimits{PushEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Changed || got.CommitSHA == "" || got.Branch != "feature" {
		t.Fatalf("publish result: %+v", got)
	}
	if !strings.Contains(got.Message, "MojoReviewer") {
		t.Fatalf("message=%q", got.Message)
	}
	remote := strings.TrimSpace(run(t, "", "git", "--git-dir", origin, "rev-parse", "refs/heads/feature"))
	if remote != got.CommitSHA {
		t.Fatalf("remote feature=%s commit=%s", remote, got.CommitSHA)
	}
	content := run(t, "", "git", "--git-dir", origin, "show", "feature:README.md")
	if !strings.Contains(content, "v3-fixed") {
		t.Fatalf("remote content=%q", content)
	}
}

func TestPublishFixNoChanges(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(seed, "a.txt"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, seed, "git", "add", "a.txt")
	run(t, seed, "git", "commit", "-m", "init")
	run(t, seed, "git", "branch", "-M", "main")
	run(t, seed, "git", "checkout", "-b", "feature")
	run(t, seed, "git", "commit", "--allow-empty", "-m", "empty")
	head := strings.TrimSpace(run(t, seed, "git", "rev-parse", "HEAD"))
	base := strings.TrimSpace(run(t, seed, "git", "rev-parse", "main"))
	run(t, seed, "git", "push", "origin", "main")
	run(t, seed, "git", "push", "-u", "origin", "feature")

	ws, err := PrepareWithBase(root, "acme/web", origin, head, "acme/web#10", "main", base)
	if err != nil {
		t.Fatal(err)
	}
	got, err := PublishFix(ws.Dir, "feature", "main", "", PublishLimits{PushEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.Changed {
		t.Fatalf("expected no changes: %+v", got)
	}
}

func TestPublishFixRejectsBaseBranch(t *testing.T) {
	_, err := PublishFix(t.TempDir(), "main", "main", "x", PublishLimits{PushEnabled: true})
	if err == nil || !strings.Contains(err.Error(), "base") {
		t.Fatalf("err=%v", err)
	}
}

func TestTruncateCommitMessage(t *testing.T) {
	if got := truncateCommitMessage("改超时"); !strings.HasPrefix(got, "MojoReviewer fix:") {
		t.Fatalf("got=%q", got)
	}
	if got := truncateCommitMessage("MojoReviewer fix: already"); got != "MojoReviewer fix: already" {
		t.Fatalf("got=%q", got)
	}
}

func TestPublishPathDenied(t *testing.T) {
	for _, path := range []string{".env", "certs/app.pem", "id_rsa", "foo/secrets.json", ".git/config"} {
		if !publishPathDenied(path) {
			t.Fatalf("%s 应拒绝", path)
		}
	}
	for _, path := range []string{"main.go", "internal/config/config.go", "README.md", "docs/secret-handling.md"} {
		if publishPathDenied(path) {
			t.Fatalf("%s 不应拒绝", path)
		}
	}
}

func TestPublishFixRejectsEnvFile(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(seed, "ok.go"), []byte("package ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, seed, "git", "add", "ok.go")
	run(t, seed, "git", "commit", "-m", "init")
	run(t, seed, "git", "branch", "-M", "main")
	run(t, seed, "git", "checkout", "-b", "feature")
	run(t, seed, "git", "commit", "--allow-empty", "-m", "head")
	head := strings.TrimSpace(run(t, seed, "git", "rev-parse", "HEAD"))
	base := strings.TrimSpace(run(t, seed, "git", "rev-parse", "main"))
	run(t, seed, "git", "push", "origin", "main")
	run(t, seed, "git", "push", "-u", "origin", "feature")

	ws, err := PrepareWithBase(root, "acme/web", origin, head, "acme/web#11", "main", base)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.Dir, ".env"), []byte("TOKEN=x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = PublishFix(ws.Dir, "feature", "main", "leak", PublishLimits{PushEnabled: true})
	if err == nil || !strings.Contains(err.Error(), "敏感路径") {
		t.Fatalf("err=%v", err)
	}
}

func TestPublishFixPushDisabled(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(seed, "a.txt"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, seed, "git", "add", "a.txt")
	run(t, seed, "git", "commit", "-m", "init")
	run(t, seed, "git", "branch", "-M", "main")
	run(t, seed, "git", "checkout", "-b", "feature")
	run(t, seed, "git", "commit", "--allow-empty", "-m", "head")
	head := strings.TrimSpace(run(t, seed, "git", "rev-parse", "HEAD"))
	base := strings.TrimSpace(run(t, seed, "git", "rev-parse", "main"))
	run(t, seed, "git", "push", "origin", "main")
	run(t, seed, "git", "push", "-u", "origin", "feature")
	ws, err := PrepareWithBase(root, "acme/web", origin, head, "acme/web#12", "main", base)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.Dir, "a.txt"), []byte("2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := PublishFix(ws.Dir, "feature", "main", "x", PublishLimits{PushEnabled: false})
	if err != nil {
		t.Fatal(err)
	}
	if got.Pushed || got.Skipped == "" {
		t.Fatalf("got=%+v", got)
	}
}

func TestPublishFixMaxChangedFiles(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(seed, "a.txt"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, seed, "git", "add", "a.txt")
	run(t, seed, "git", "commit", "-m", "init")
	run(t, seed, "git", "branch", "-M", "main")
	run(t, seed, "git", "checkout", "-b", "feature")
	run(t, seed, "git", "commit", "--allow-empty", "-m", "head")
	head := strings.TrimSpace(run(t, seed, "git", "rev-parse", "HEAD"))
	base := strings.TrimSpace(run(t, seed, "git", "rev-parse", "main"))
	run(t, seed, "git", "push", "origin", "main")
	run(t, seed, "git", "push", "-u", "origin", "feature")
	ws, err := PrepareWithBase(root, "acme/web", origin, head, "acme/web#maxf", "main", base)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		name := filepath.Join(ws.Dir, fmt.Sprintf("f%d.txt", i))
		if err := os.WriteFile(name, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, err = PublishFix(ws.Dir, "feature", "main", "too many", PublishLimits{PushEnabled: true, MaxChangedFiles: 2})
	if err == nil || !strings.Contains(err.Error(), "超过上限") {
		t.Fatalf("err=%v", err)
	}
}

func TestPublishFixMaxDiffBytes(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(seed, "a.txt"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, seed, "git", "add", "a.txt")
	run(t, seed, "git", "commit", "-m", "init")
	run(t, seed, "git", "branch", "-M", "main")
	run(t, seed, "git", "checkout", "-b", "feature")
	run(t, seed, "git", "commit", "--allow-empty", "-m", "head")
	head := strings.TrimSpace(run(t, seed, "git", "rev-parse", "HEAD"))
	base := strings.TrimSpace(run(t, seed, "git", "rev-parse", "main"))
	run(t, seed, "git", "push", "origin", "main")
	run(t, seed, "git", "push", "-u", "origin", "feature")
	ws, err := PrepareWithBase(root, "acme/web", origin, head, "acme/web#maxd", "main", base)
	if err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("Z", 5000)
	if err := os.WriteFile(filepath.Join(ws.Dir, "a.txt"), []byte(big+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = PublishFix(ws.Dir, "feature", "main", "big", PublishLimits{PushEnabled: true, MaxDiffBytes: 100})
	if err == nil || !strings.Contains(err.Error(), "超过上限") {
		t.Fatalf("err=%v", err)
	}
}
