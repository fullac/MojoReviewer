package projectmem

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSelectKnowledgeFiles(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("README.md", "# hello")
	write("AGENTS.md", "only review")
	write("docs/guide.md", "guide")
	write("docs/nested/arch.md", "arch")
	write("src/main.go", "package main")
	write(".env.example", "SECRET=1")
	write("docs/secret-notes.md", "do not index name match alone")
	write(".github/CONTRIBUTING.md", "contrib")
	write("vendor/README.md", "skip vendor by not walking it")

	got, err := SelectKnowledgeFiles(root, 40, 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"README.md":                true,
		"AGENTS.md":                true,
		"docs/guide.md":            true,
		"docs/nested/arch.md":      true,
		".github/CONTRIBUTING.md":  true,
		"docs/secret-notes.md":     true, // 文件名含 secret：应被跳过
	}
	seen := map[string]bool{}
	for _, c := range got {
		seen[c.Rel] = true
	}
	for _, rel := range []string{"README.md", "AGENTS.md", "docs/guide.md", "docs/nested/arch.md", ".github/CONTRIBUTING.md"} {
		if !seen[rel] {
			t.Fatalf("缺少 %s: %#v", rel, seen)
		}
	}
	if seen["src/main.go"] {
		t.Fatalf("不应索引代码文件")
	}
	if seen[".env.example"] {
		t.Fatalf("不应索引 .env*")
	}
	if seen["docs/secret-notes.md"] {
		t.Fatalf("文件名含 secret 应跳过: %#v", want)
	}
	if seen["vendor/README.md"] {
		t.Fatalf("不应走到 vendor")
	}
}

func TestSelectKnowledgeFilesSkipsHuge(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "README.md")
	if err := os.WriteFile(path, make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := SelectKnowledgeFiles(root, 10, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("超大文件应跳过: %#v", got)
	}
}
