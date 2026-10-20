package projectmem

import (
	"os"
	"path/filepath"
	"testing"
)

func knowledgeWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func knowledgeSymlink(t *testing.T, target, path string) {
	t.Helper()
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
}

func TestSelectKnowledgeFilesContainsSymlinkTargets(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	knowledgeWrite(t, filepath.Join(outside, "README.md"), "external private content")
	knowledgeWrite(t, filepath.Join(outside, "CONTRIBUTING.md"), "external contribution content")
	knowledgeWrite(t, filepath.Join(root, "docs/inside.md"), "internal content")
	knowledgeSymlink(t, filepath.Join(outside, "README.md"), filepath.Join(root, "README.md"))
	knowledgeSymlink(t, outside, filepath.Join(root, ".github"))
	knowledgeSymlink(t, filepath.Join(outside, "README.md"), filepath.Join(root, "docs/external.md"))
	knowledgeSymlink(t, "external.md", filepath.Join(root, "docs/chained.md"))
	knowledgeSymlink(t, filepath.Join(outside, "missing.md"), filepath.Join(root, "docs/broken.md"))
	knowledgeSymlink(t, "docs/inside.md", filepath.Join(root, "AGENTS.md"))
	want := map[string]bool{"AGENTS.md": true, "docs/inside.md": true}
	// A symlink to the checkout itself is a valid caller-provided root.
	alias := filepath.Join(t.TempDir(), "checkout")
	knowledgeSymlink(t, root, alias)
	for _, workDir := range []string{root, alias} {
		got, err := SelectKnowledgeFiles(workDir, 40, 64<<10)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(want) {
			t.Fatalf("external documents reached the index candidates: %#v", got)
		}
		for _, candidate := range got {
			if !want[candidate.Rel] {
				t.Fatalf("unexpected index candidate %q", candidate.Rel)
			}
		}
	}
}

func TestSelectKnowledgeFilesSkipsExternalDocsDirectory(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	knowledgeWrite(t, filepath.Join(outside, "guide.md"), "external private content")
	knowledgeSymlink(t, outside, filepath.Join(root, "docs"))
	got, err := SelectKnowledgeFiles(root, 40, 64<<10)
	if err != nil || len(got) != 0 {
		t.Fatalf("external docs directory should not be indexed: %#v %v", got, err)
	}
}

func TestKnowledgeFileRechecksTargetBeforeIndexRead(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	path := filepath.Join(root, "README.md")
	knowledgeWrite(t, path, "safe original content")
	knowledgeWrite(t, filepath.Join(outside, "private.md"), "external private content")
	candidates, err := SelectKnowledgeFiles(root, 40, 64<<10)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("expected the original README candidate: %#v %v", candidates, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	knowledgeSymlink(t, filepath.Join(outside, "private.md"), path)
	if abs, _, err := resolveKnowledgeFile(root, candidates[0].Rel); err == nil || abs != "" {
		t.Fatalf("index read accepted a symlink replaced after selection: %s %v", abs, err)
	}
}

func TestKnowledgeFileRejectsSensitiveTargetsAndDirectories(t *testing.T) {
	root := t.TempDir()
	knowledgeWrite(t, filepath.Join(root, ".env"), "private value")
	knowledgeWrite(t, filepath.Join(root, "docs/safe.md"), "safe document")
	knowledgeSymlink(t, ".env", filepath.Join(root, "README.md"))
	knowledgeSymlink(t, "docs", filepath.Join(root, "AGENTS.md"))
	for _, rel := range []string{"README.md", "AGENTS.md", "../outside.md"} {
		if _, _, err := resolveKnowledgeFile(root, rel); err == nil {
			t.Fatalf("unsafe or non-regular target accepted: %s", rel)
		}
	}
	got, err := SelectKnowledgeFiles(root, 40, 64<<10)
	if err != nil || len(got) != 1 || got[0].Rel != "docs/safe.md" {
		t.Fatalf("expected only a regular safe document: %#v %v", got, err)
	}
}
