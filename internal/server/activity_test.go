package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mojoreviewer/mojoreviewer/internal/store"
)

func autoRequest(s *Server, head string) *httptest.ResponseRecorder {
	body := `{"action":"synchronize","pull_request":{"number":7,"head":{"sha":"` + head + `"}},"repository":{"full_name":"acme/web"}}`
	mac := hmac.New(sha256.New, []byte(s.cfg.GitHub.WebhookSecret))
	mac.Write([]byte(body))
	req := httptest.NewRequest("POST", "/webhook/github", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	w := httptest.NewRecorder()
	s.handleWebhook(w, req)
	return w
}

func TestPendingDuplicateAndFullQueue(t *testing.T) {
	s, _, _, _ := testServer(t, "", "")
	if w := autoRequest(s, "one"); w.Code != 202 {
		t.Fatal(w.Code)
	}
	if w := autoRequest(s, "one"); w.Code != 200 {
		t.Fatal("pending duplicate", w.Code)
	}

	item := <-s.jobs
	for i := 0; i < cap(s.jobs); i++ {
		s.jobs <- item
	}
	if w := autoRequest(s, "two"); w.Code != 429 {
		t.Fatal("full queue", w.Code)
	}
	if _, ok := s.store.Get("acme/web#7"); ok {
		t.Fatal("unstarted head was reserved")
	}
}

func TestFailureActivityDoesNotExposeRawError(t *testing.T) {
	s, _, _, _ := testServer(t, "", "HTTP 403: forbidden token=private-test-value")
	s.runOne(context.Background(), queueComment(t, s))
	raw, err := os.ReadFile(filepath.Join(s.cfg.DataDir, "activity.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rows []store.Activity
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].State != "failed" || rows[0].Stage != "metadata" {
		t.Fatalf("wrong receipt: %s", raw)
	}
	if strings.Contains(string(raw), "private-test-value") {
		t.Fatal("raw error leaked")
	}
}

func TestStartedFailedHeadReservedAcrossRestart(t *testing.T) {
	s, _, _, _ := testServer(t, "", "")
	if w := autoRequest(s, "one"); w.Code != 202 {
		t.Fatal(w.Code)
	}
	item := <-s.jobs
	origin := t.TempDir()
	runGit(t, origin, "init")
	item.CloneURL = "file://" + origin
	s.runOne(context.Background(), item)
	s.pending = map[string]bool{}
	if w := autoRequest(s, "one"); w.Code != 200 {
		t.Fatal("started head duplicated", w.Code)
	}
}
