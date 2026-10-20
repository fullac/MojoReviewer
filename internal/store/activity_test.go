package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestActivityBoundedAtomicAndPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reviews.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 260; i++ {
		if err := s.Activity(Activity{ID: strconv.Itoa(i), State: "queued"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Activity(Activity{ID: "259", State: "completed", CommentURL: "https://example.test/comment"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(filepath.Dir(path), "activity.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	raw, _ := os.ReadFile(filepath.Join(filepath.Dir(path), "activity.json"))
	var rows []Activity
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 256 || rows[0].ID != "4" || rows[255].State != "completed" {
		t.Fatal("snapshot not bounded or updated")
	}
}

func TestStartupInterruptsOnlyUnfinishedAttempts(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "reviews.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"queued", "running", "completed", "failed"} {
		if err := s.Activity(Activity{ID: state, State: state}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.InterruptActivities(); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(filepath.Dir(s.path), "activity.json"))
	var rows []Activity
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	if rows[0].State != "interrupted" || rows[1].State != "interrupted" || rows[2].State != "completed" || rows[3].State != "failed" {
		t.Fatalf("bad startup states: %s", raw)
	}
	if seen, err := s.HasActivity("running"); err != nil || !seen {
		t.Fatal("transport receipt lost", err)
	}
	if seen, err := s.HasActivity("queued"); err != nil || seen {
		t.Fatal("unstarted transport must remain redeliverable", err)
	}
}
