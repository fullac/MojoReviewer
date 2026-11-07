// Verify reaction routing, host selection, and bounded failure handling offline.
package github

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReactStarted(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	script := "#!/bin/sh\nprintf 'GH_HOST=%s\\n' \"$GH_HOST\" >> \"$REACTION_LOG\"\nprintf '%s\\n' \"$@\" >> \"$REACTION_LOG\"\n"
	gh := filepath.Join(dir, "gh")
	if err := os.WriteFile(gh, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("REACTION_LOG", log)
	t.Setenv("GH_HOST", "inherited.example")
	for _, tc := range []struct {
		name, comment, host, target string
		id                          int64
	}{
		{"PR", "", "", "issues/7", 0},
		{"comment", "@mojo review", " ghe.example ", "issues/comments/6382461298", 6382461298},
		{"GUI", "@mojo review", "", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_ = os.Remove(log)
			item := Review{Repo: "acme/web", Number: 7, Comment: tc.comment, CommentID: tc.id}
			if err := ReactStarted(context.Background(), item, tc.host); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(log)
			if tc.target == "" {
				if !os.IsNotExist(err) {
					t.Fatal("local request unexpectedly called GitHub")
				}
				return
			}
			want := "GH_HOST=inherited.example\napi\n"
			if tc.host != "" {
				want += "--hostname\nghe.example\n"
			}
			want += "--method\nPOST\nrepos/acme/web/" + tc.target + "/reactions\n--raw-field\ncontent=eyes\n"
			if err != nil || string(raw) != want {
				t.Fatalf("reaction command: %s, %v", raw, err)
			}
		})
	}
	item := Review{Repo: "acme/web", Number: 7}
	if err := os.WriteFile(gh, []byte("#!/bin/sh\necho private-response >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := ReactStarted(context.Background(), item, ""); err == nil || strings.Contains(err.Error(), "private-response") {
		t.Fatalf("failure must omit raw stderr: %v", err)
	}
	if err := os.WriteFile(gh, []byte("#!/bin/sh\nexec sleep 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := ReactStarted(ctx, item, ""); err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("reaction ignored cancellation: %v", err)
	}
}
