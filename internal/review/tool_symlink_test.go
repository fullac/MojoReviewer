package review

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func symlink(t *testing.T, target, path string) {
	t.Helper()
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
}

func TestReadFileContainsSymlinksWithinCheckout(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	write(t, filepath.Join(dir, "inside/data.txt"), "inside text\n")
	write(t, filepath.Join(outside, "secret.txt"), "external secret\n")
	symlink(t, filepath.Join(outside, "secret.txt"), filepath.Join(dir, "external.txt"))
	symlink(t, outside, filepath.Join(dir, "external-dir"))
	symlink(t, "external-dir", filepath.Join(dir, "external-chain"))
	symlink(t, "inside/data.txt", filepath.Join(dir, "internal.txt"))
	symlink(t, "inside", filepath.Join(dir, "internal-dir"))
	symlink(t, filepath.Join(outside, "missing"), filepath.Join(dir, "broken-external"))
	scope := ToolScope{Dir: dir}
	for _, path := range []string{"external.txt", "external-dir/secret.txt", "external-chain/secret.txt", "external-dir/missing.go", "broken-external"} {
		if _, err := scope.clean(path); err == nil {
			t.Errorf("clean accepted an external symlink: %s", path)
		}
		if out, err := scope.ReadFile(path, 0, 0); err == nil || strings.Contains(out, "external secret") {
			t.Errorf("ReadFile followed an external symlink %s: %q %v", path, out, err)
		}
	}
	for _, path := range []string{"internal.txt", "internal-dir/data.txt"} {
		out, err := scope.ReadFile(path, 0, 0)
		if err != nil || !strings.Contains(out, "inside text") {
			t.Errorf("internal symlink should be readable %s: %q %v", path, out, err)
		}
	}
}

func symlinkReviewRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "git", "init", "-b", "trunk")
	run(t, dir, "git", "config", "user.name", "test")
	run(t, dir, "git", "config", "user.email", "test@example.com")
	run(t, dir, "git", "commit", "--allow-empty", "-m", "base")
	run(t, dir, "git", "update-ref", mergeBaseRef, "HEAD")
	return dir
}

func TestSymbolAndTestToolsDoNotFollowExternalSymlinks(t *testing.T) {
	dir, outside := symlinkReviewRepo(t), t.TempDir()
	write(t, filepath.Join(outside, "external.go"), "package leak; func OutsideSymbol() {}\n")
	write(t, filepath.Join(outside, "external_test.go"), "package leak\n")
	write(t, filepath.Join(dir, "safe.go"), "package safe\nfunc InsideSymbol() {}\n")
	write(t, filepath.Join(dir, "safe_test.go"), "package safe\n")
	symlink(t, filepath.Join(outside, "external.go"), filepath.Join(dir, "external.go"))
	symlink(t, filepath.Join(outside, "external_test.go"), filepath.Join(dir, "external_test.go"))
	symlink(t, outside, filepath.Join(dir, "external-dir"))
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-m", "head")
	scope := ToolScope{Dir: dir}
	out, err := scope.ChangedSymbols()
	if err != nil || !strings.Contains(out, "InsideSymbol") || strings.Contains(out, "OutsideSymbol") {
		t.Fatalf("symbols should include safe code only: %s %v", out, err)
	}
	out, err = scope.TestsAround("")
	if err != nil || !strings.Contains(out, "safe_test.go") || strings.Contains(out, "external") {
		t.Fatalf("test discovery should stay within checkout: %s %v", out, err)
	}
	if out, err := scope.TestsAround("external-dir/external.go"); err == nil {
		t.Fatalf("test discovery accepted an external directory symlink: %s", out)
	}
}

func TestFileDiffStillReadsDeletedFiles(t *testing.T) {
	dir := symlinkReviewRepo(t)
	path := filepath.Join(dir, "removed/old.go")
	write(t, path, "package old\n")
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-m", "old file")
	run(t, dir, "git", "update-ref", mergeBaseRef, "HEAD")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "git", "commit", "-am", "delete file and parent directory")
	out, err := (ToolScope{Dir: dir}).FileDiff("removed/old.go")
	if err != nil || !strings.Contains(out, "deleted file mode") || !strings.Contains(out, "-package old") {
		t.Fatalf("deleted file patch should remain available: %s %v", out, err)
	}
}

func TestGrepDoesNotFollowExternalSymlinks(t *testing.T) {
	for _, trackedSymlink := range []bool{false, true} {
		name := "tracked file replaced by symlink"
		if trackedSymlink {
			name = "tracked symlink"
		}
		t.Run(name, func(t *testing.T) {
			dir, outside := symlinkReviewRepo(t), t.TempDir()
			path := filepath.Join(dir, "linked.txt")
			target := filepath.Join(outside, "secret.txt")
			write(t, target, "EXTERNAL_SECRET_CONTENT\n")
			if trackedSymlink {
				symlink(t, target, path)
			} else {
				write(t, path, "ordinary tracked file\n")
			}
			run(t, dir, "git", "add", ".")
			run(t, dir, "git", "commit", "-m", "head")
			if !trackedSymlink {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				symlink(t, target, path)
			}
			out, err := (ToolScope{Dir: dir}).Grep("EXTERNAL_SECRET_CONTENT", "")
			if err != nil || strings.Contains(out, "EXTERNAL_SECRET_CONTENT") {
				t.Fatalf("grep followed an external symlink: %s %v", out, err)
			}
		})
	}
}
