package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestReviewContextPrivateAndBoundToAttempt(t *testing.T) {
	root := t.TempDir()
	s, err := Open(filepath.Join(root, "reviews.json"))
	if err != nil {
		t.Fatal(err)
	}
	value := ReviewContext{AttemptID: "../../delivery", Repo: "acme/web", Number: 7, SessionID: "session-1", MessageStart: 12, SystemPrompt: "system fixture", TaskPrompt: "task fixture", Provider: "provider", Model: "model"}
	if err := s.SaveReviewContext(value); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(value.AttemptID))
	path := filepath.Join(root, "review_context", hex.EncodeToString(hash[:])+".json")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("context must be private", err)
	}
	dir, _ := os.Stat(filepath.Dir(path))
	if dir.Mode().Perm() != 0700 {
		t.Fatal("directory must be private")
	}
	raw, _ := os.ReadFile(path)
	var got ReviewContext
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.CreatedAt == "" {
		t.Fatal("missing capture timestamp")
	}
	value.CreatedAt = got.CreatedAt
	if got != value {
		t.Fatal("context lost association or input")
	}
	value.SessionID = ""
	if err := s.SaveReviewContext(value); err == nil {
		t.Fatal("missing session accepted")
	}
}
