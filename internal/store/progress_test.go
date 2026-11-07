package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProgressKeepsSanitizedEvents(t *testing.T) {
	dir := t.TempDir()
	id := "github:abc-123"
	err := AppendProgress(dir, id, ProgressEvent{
		Kind:    "tool",
		Summary: "read_repo_file a.go 1-40\nsecret sk-test /home/user/data/workspaces/repo",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := AppendProgress(dir, id, ProgressEvent{Kind: " ", Summary: "   "}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < progressEventLimit; i++ {
		if err := AppendProgress(dir, id, ProgressEvent{Kind: "model", Summary: "第 1 次模型请求"}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := ReadProgress(dir, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != progressEventLimit || rows[0].Kind != "model" {
		t.Fatalf("limit not applied: %d %s", len(rows), rows[0].Kind)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "progress", id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "\nsecret") || strings.Contains(string(raw), "sk-test\n") {
		t.Fatal("newline leaked")
	}
	info, err := os.Stat(filepath.Join(dir, "progress", id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	missing, err := ReadProgress(dir, "missing-attempt")
	if err != nil || len(missing) != 0 {
		t.Fatal(missing, err)
	}
	if _, err := ReadProgress(dir, "../activity"); err == nil {
		t.Fatal("path escape accepted")
	}
	var decoded []ProgressEvent
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
}
