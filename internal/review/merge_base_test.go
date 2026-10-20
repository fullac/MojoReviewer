package review

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// trunk advances independently after the PR branches off. Comparing trunk's
// tip with the PR head would incorrectly report its changes as reverted.
func divergedReviewRepo(t *testing.T) (dir, ancestor, base, head string) {
	t.Helper()
	dir = t.TempDir()
	run(t, dir, "git", "init", "-b", "trunk")
	run(t, dir, "git", "config", "user.name", "test")
	run(t, dir, "git", "config", "user.email", "test@example.com")
	write(t, filepath.Join(dir, "feature/calc.go"), "package feature\nfunc Calc() int { return 1 }\n")
	write(t, filepath.Join(dir, "feature/calc_test.go"), "package feature\n")
	write(t, filepath.Join(dir, "unrelated/service.go"), "package unrelated\nfunc Serve() int { return 1 }\n")
	write(t, filepath.Join(dir, "unrelated/service_test.go"), "package unrelated\n")
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-m", "ancestor")
	ancestor = strings.TrimSpace(run(t, dir, "git", "rev-parse", "HEAD"))

	run(t, dir, "git", "checkout", "-b", "feature")
	write(t, filepath.Join(dir, "feature/calc.go"), "package feature\nfunc Calc() int { return 2 }\n")
	run(t, dir, "git", "commit", "-am", "PR change")
	head = strings.TrimSpace(run(t, dir, "git", "rev-parse", "HEAD"))

	run(t, dir, "git", "checkout", "trunk")
	write(t, filepath.Join(dir, "unrelated/service.go"), "package unrelated\nfunc Serve() int { return 2 }\n")
	write(t, filepath.Join(dir, "base-only.go"), "package base\n")
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-m", "base advances")
	base = strings.TrimSpace(run(t, dir, "git", "rev-parse", "HEAD"))
	return dir, ancestor, base, head
}

func TestReviewDiffsExcludeChangesOnlyOnBase(t *testing.T) {
	origin, ancestor, base, head := divergedReviewRepo(t)
	for _, reuseShallow := range []bool{false, true} {
		name := "fresh"
		if reuseShallow {
			name = "existing shallow checkout"
		}
		t.Run(name, func(t *testing.T) {
			dataDir := t.TempDir()
			// file:// makes --depth effective for local repositories.
			cloneURL := "file://" + origin
			if reuseShallow {
				ws, err := Prepare(dataDir, "acme/web", cloneURL, head, "review")
				if err != nil {
					t.Fatal(err)
				}
				if got := strings.TrimSpace(run(t, ws.Dir, "git", "rev-parse", "--is-shallow-repository")); got != "true" {
					t.Fatalf("fixture should be shallow, got %s", got)
				}
			}
			ws, err := PrepareWithBase(dataDir, "acme/web", cloneURL, head, "review", "trunk", base)
			if err != nil {
				t.Fatal(err)
			}
			for ref, want := range map[string]string{"HEAD": head, baseRef: base, mergeBaseRef: ancestor} {
				if got := strings.TrimSpace(run(t, ws.Dir, "git", "rev-parse", ref)); got != want {
					t.Fatalf("%s = %s, want %s", ref, got, want)
				}
			}
			if err := exec.Command("git", "-C", ws.Dir, "symbolic-ref", "-q", "HEAD").Run(); err == nil {
				t.Fatal("review checkout must be detached")
			}
			if out := strings.TrimSpace(run(t, ws.Dir, "git", "branch", "--list", "main")); out != "" {
				t.Fatalf("fixture must not have local main: %s", out)
			}
			// Once preparation succeeds all diff reads are local and use the
			// pinned ancestor, even if the remote is no longer reachable.
			run(t, ws.Dir, "git", "remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing.git"))
			scope := ToolScope{Dir: ws.Dir}
			checks := []struct {
				name string
				read func() (string, error)
				want string
			}{
				{"diff", func() (string, error) { return Diff(ws.Dir, base) }, "+func Calc() int { return 2 }"},
				{"stat", scope.Stat, "total_files: 1"},
				{"file patch", func() (string, error) { return scope.FileDiff("feature/calc.go") }, "+func Calc() int { return 2 }"},
				{"symbols", scope.ChangedSymbols, "Calc"},
				{"tests", func() (string, error) { return scope.TestsAround("") }, "feature/calc_test.go"},
			}
			for _, check := range checks {
				out, err := check.read()
				if err != nil {
					t.Fatalf("%s: %v", check.name, err)
				}
				if !strings.Contains(out, check.want) || strings.Contains(out, "unrelated/") || strings.Contains(out, "base-only.go") {
					t.Fatalf("%s included non-PR changes or missed PR change:\n%s", check.name, out)
				}
			}
			out, err := scope.FileDiff("unrelated/service.go")
			if err != nil || !strings.Contains(out, "没有差异") {
				t.Fatalf("base-only change must have no PR patch: %s %v", out, err)
			}
			if _, err := Diff(ws.Dir, ancestor); err == nil {
				t.Fatal("Diff should reject a base SHA different from the prepared base")
			}
		})
	}
}

func TestPrepareRejectsUnrelatedHistoryAndClearsOldMergeBase(t *testing.T) {
	origin, _, base, head := divergedReviewRepo(t)
	dataDir := t.TempDir()
	ws, err := PrepareWithBase(dataDir, "acme/web", "file://"+origin, head, "review", "trunk", base)
	if err != nil {
		t.Fatal(err)
	}
	run(t, origin, "git", "checkout", "--orphan", "unrelated-root")
	run(t, origin, "git", "commit", "--allow-empty", "-m", "unrelated root")
	unrelated := strings.TrimSpace(run(t, origin, "git", "rev-parse", "HEAD"))
	if _, err := PrepareWithBase(dataDir, "acme/web", "file://"+origin, unrelated, "review", "trunk", base); err == nil || !strings.Contains(err.Error(), "共同祖先") {
		t.Fatalf("unrelated histories should fail preparation, got %v", err)
	}
	if err := exec.Command("git", "-C", ws.Dir, "show-ref", "--verify", "--quiet", mergeBaseRef).Run(); err == nil {
		t.Fatal("failed preparation retained the previous review's merge-base")
	}
}

func TestDiffToolsRequirePinnedMergeBase(t *testing.T) {
	dir, _, base, head := divergedReviewRepo(t)
	run(t, dir, "git", "checkout", "--detach", head)
	run(t, dir, "git", "update-ref", baseRef, base)
	scope := ToolScope{Dir: dir}
	checks := []struct {
		name string
		read func() (string, error)
	}{
		{"diff", func() (string, error) { return Diff(dir, base) }},
		{"stat", scope.Stat},
		{"file patch", func() (string, error) { return scope.FileDiff("feature/calc.go") }},
		{"symbols", scope.ChangedSymbols},
		{"tests", func() (string, error) { return scope.TestsAround("") }},
	}
	for _, check := range checks {
		if _, err := check.read(); err == nil {
			t.Errorf("%s silently accepted a missing merge-base", check.name)
		}
	}
}
