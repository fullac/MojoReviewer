package projectmem

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "project_memory.json")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	err = st.Put("acme/web", RepoState{
		HeadSHA: "abc",
		Indexed: map[string]FileState{
			"README.md": {Size: 12, ModTime: now},
		},
		SummaryKey: "project-memory:acme/web",
	})
	if err != nil {
		t.Fatal(err)
	}
	st2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := st2.Get("acme/web")
	if !ok || got.HeadSHA != "abc" || got.Indexed["README.md"].Size != 12 {
		t.Fatalf("roundtrip: %+v ok=%v", got, ok)
	}
}
