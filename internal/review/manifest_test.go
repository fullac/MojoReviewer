// 验证清单的大小限制、特殊路径和符号链接边界，以及删除与二进制文件的降级。
package review

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManifestMetadataAndBoundaries(t *testing.T) {
	dir := symlinkReviewRepo(t)
	write(t, filepath.Join(dir, "old.go"), "package old\n")
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-m", "old")
	run(t, dir, "git", "update-ref", mergeBaseRef, "HEAD")
	os.Remove(filepath.Join(dir, "old.go"))
	write(t, filepath.Join(dir, "auth.go"), "package safe\nfunc CheckPermission() { /* PATCH_SECRET_NOT_IN_MANIFEST */ }\n")
	write(t, filepath.Join(dir, "auth_test.go"), "package safe\n")
	write(t, filepath.Join(dir, "line\n<break>.go"), "package safe\nfunc OddPath() {}\n")
	write(t, filepath.Join(dir, "data.bin"), "binary\x00data")
	outside := t.TempDir()
	write(t, filepath.Join(outside, "secret.go"), "package leak\nfunc OutsideSecret() {}\n")
	symlink(t, filepath.Join(outside, "secret.go"), filepath.Join(dir, "link.go"))
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-m", "head")
	m, err := BuildManifest(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(m)
	if !m.Complete || m.TotalFiles != 6 || m.OmittedFiles != 0 {
		t.Fatalf("bad map: %s", raw)
	}
	if strings.Contains(string(raw), "PATCH_SECRET") || strings.Contains(string(raw), "OutsideSecret") || strings.Contains(string(raw), outside) {
		t.Fatalf("manifest leaked source: %s", raw)
	}
	seen := map[string]ChangedFile{}
	for _, f := range m.Files {
		seen[f.Path] = f
	}
	if len(seen["auth.go"].Symbols) != 1 || seen["auth.go"].Symbols[0].Name != "CheckPermission" || len(seen["auth.go"].Tests) == 0 {
		t.Fatalf("missing symbols/tests: %s", raw)
	}
	if !seen["link.go"].DetailsPartial || !seen["old.go"].DetailsPartial || !seen["data.bin"].Binary || len(seen["line\n<break>.go"].Symbols) != 1 {
		t.Fatalf("unsafe or missing metadata: %s", raw)
	}
}

func TestManifestSizeAndMissingBaseline(t *testing.T) {
	dir := symlinkReviewRepo(t)
	for i := 0; i < 240; i++ {
		write(t, filepath.Join(dir, fmt.Sprintf("%03d-%s.txt", i, strings.Repeat("long", 30))), "text\n")
	}
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-m", "large")
	m, err := BuildManifest(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(m)
	if len(raw) > ManifestMaxBytes || m.Complete || m.OmittedFiles != 240-len(m.Files) || len(m.Files) > manifestMaxFiles {
		t.Fatalf("unbounded manifest: files=%d bytes=%d", len(m.Files), len(raw))
	}
	run(t, dir, "git", "update-ref", "-d", mergeBaseRef)
	if _, err := BuildManifest(context.Background(), dir); err == nil {
		t.Fatal("missing baseline silently accepted")
	}
}
