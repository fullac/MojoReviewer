package store

import (
	"path/filepath"
	"testing"
)

func TestStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reviews.json")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Put("acme/web#1", Record{Repo: "acme/web", Number: 1, HeadSHA: "abc", SessionID: "sess"}); err != nil {
		t.Fatal(err)
	}
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := again.Get("acme/web#1")
	if !ok || got.SessionID != "sess" || got.HeadSHA != "abc" {
		t.Fatalf("记录错误: %+v %v", got, ok)
	}
}
