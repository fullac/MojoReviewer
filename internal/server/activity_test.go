package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/mojoreviewer/mojoreviewer/internal/agent"
	"github.com/mojoreviewer/mojoreviewer/internal/github"
	"github.com/mojoreviewer/mojoreviewer/internal/review"
	"github.com/mojoreviewer/mojoreviewer/internal/store"
)

func autoRequest(s *Server, head string) *httptest.ResponseRecorder {
	return autoDeliveryRequest(s, head, "")
}

func autoDeliveryRequest(s *Server, head, delivery string) *httptest.ResponseRecorder {
	body := `{"action":"synchronize","pull_request":{"number":7,"head":{"sha":"` + head + `"}},"repository":{"full_name":"acme/web"}}`
	mac := hmac.New(sha256.New, []byte(s.cfg.GitHub.WebhookSecret))
	mac.Write([]byte(body))
	req := httptest.NewRequest("POST", "/webhook/github", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", "pull_request")
	if delivery != "" {
		req.Header.Set("X-GitHub-Delivery", delivery)
	}
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	w := httptest.NewRecorder()
	s.handleWebhook(w, req)
	return w
}

func TestConcurrentAutomaticDeliveriesQueueOnce(t *testing.T) {
	s, _, _, _ := testServer(t, "", "")
	var group sync.WaitGroup
	statuses := make(chan int, 24)
	for i := 0; i < cap(statuses); i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			statuses <- autoRequest(s, "same-head").Code
		}()
	}
	group.Wait()
	close(statuses)
	accepted := 0
	for status := range statuses {
		if status == 202 {
			accepted++
		} else if status != 200 {
			t.Fatalf("unexpected response: %d", status)
		}
	}
	if accepted != 1 || len(s.jobs) != 1 || cap(s.jobs) != 32 {
		t.Fatalf("accepted=%d queued=%d capacity=%d", accepted, len(s.jobs), cap(s.jobs))
	}
}

func TestQueueReceiptFailureDoesNotAcceptTask(t *testing.T) {
	s, _, _, _ := testServer(t, "", "")
	if err := os.Mkdir(filepath.Join(s.cfg.DataDir, "activity.json"), 0700); err != nil {
		t.Fatal(err)
	}
	if w := autoRequest(s, "head"); w.Code != 500 {
		t.Fatal("unrecorded task was accepted", w.Code)
	}
	if len(s.jobs) != 0 || len(s.pending) != 0 {
		t.Fatal("receipt failure still queued or reserved a task")
	}
}

func TestFailedAutomaticHeadHistorySurvivesHeadChanges(t *testing.T) {
	s, _, _, _ := testServer(t, "", "")
	origin := t.TempDir()
	runGit(t, origin, "init")
	for _, head := range []string{"A", "B"} {
		if w := autoRequest(s, head); w.Code != 202 {
			t.Fatal("new automatic head was not queued", w.Code)
		}
		item := <-s.jobs
		item.CloneURL = "file://" + origin
		s.runOne(context.Background(), item)
	}
	// Reloading the store simulates a process restart after the failed attempts.
	st, err := store.Open(filepath.Join(s.cfg.DataDir, "reviews.json"))
	if err != nil {
		t.Fatal(err)
	}
	s.store, s.pending = st, map[string]bool{}
	if w := autoRequest(s, "A"); w.Code != 200 {
		t.Fatal("A -> B -> A reran a failed automatic head", w.Code)
	}
	if len(s.jobs) != 0 {
		t.Fatal("historical duplicate entered the queue")
	}
	raw, err := os.ReadFile(filepath.Join(s.cfg.DataDir, "activity.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rows []store.Activity
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].State != "failed" || rows[1].State != "failed" {
		t.Fatalf("failed attempts were lost: %s", raw)
	}
}

func TestRestartAllowsUnstartedDeliveryToBeRequeued(t *testing.T) {
	s, _, _, _ := testServer(t, "", "")
	if w := autoDeliveryRequest(s, "head", "queued-delivery"); w.Code != 202 {
		t.Fatal(w.Code)
	}
	<-s.jobs
	s.pending = map[string]bool{}
	if err := s.store.InterruptActivities(); err != nil {
		t.Fatal(err)
	}
	if w := autoDeliveryRequest(s, "head", "queued-delivery"); w.Code != 202 {
		t.Fatal("unstarted delivery was lost after restart", w.Code)
	}
	if w := autoDeliveryRequest(s, "head", "queued-delivery"); w.Code != 200 {
		t.Fatal("same live delivery was queued twice", w.Code)
	}
}

func TestExplicitReviewRetriesFailedAutomaticHeadAndReusesSession(t *testing.T) {
	s, runner, _, _ := testServer(t, "", "")
	item := localReviewItem(t)
	runner.reviewResult = &agent.ReviewResult{SessionID: "recoverable"}
	runner.reviewErr = errors.New("model interrupted")
	s.runOne(context.Background(), item)
	if w := autoRequest(s, item.HeadSHA); w.Code != 200 {
		t.Fatal("failed automatic attempt was retried automatically", w.Code)
	}
	payload, err := json.Marshal(map[string]any{
		"number": item.Number,
		"base": map[string]any{"ref": item.BaseBranch, "sha": item.BaseSHA,
			"repo": map[string]string{"full_name": item.Repo, "clone_url": item.CloneURL}},
		"head": map[string]string{"sha": item.HeadSHA},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("MOJO_TEST_RESPONSE"), payload, 0600); err != nil {
		t.Fatal(err)
	}
	runner.reviewResult = &agent.ReviewResult{SessionID: "recoverable", Text: (&review.Notes{}).Render(), Noted: true}
	runner.reviewErr = nil
	s.runOne(context.Background(), queueComment(t, s))
	if len(runner.inputs) != 2 || runner.inputs[1].SessionID != "recoverable" {
		t.Fatalf("explicit retry did not reuse the failed session: %+v", runner.inputs)
	}
	raw, err := os.ReadFile(filepath.Join(s.cfg.DataDir, "activity.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rows []store.Activity
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].State != "failed" || rows[1].State != "completed" {
		t.Fatalf("explicit retry overwrote the failed attempt: %s", raw)
	}
}

func TestAllConfiguredCredentialsAreRejectedBeforeCommentCommand(t *testing.T) {
	s, _, _, commentPath := testServer(t, "", "")
	s.cfg.Agent.APIKey = "fixture-private-model-key"
	s.cfg.GitHub.Token = "fixture-private-github-key"
	s.cfg.GitHub.WebhookSecret = "fixture-private-webhook-key"
	for _, secret := range []string{s.cfg.Agent.APIKey, s.cfg.GitHub.Token, s.cfg.GitHub.WebhookSecret} {
		if _, err := s.postComment(github.Review{Repo: "acme/web", Number: 7}, "审查意见："+secret); err == nil || strings.Contains(err.Error(), secret) {
			t.Fatal("configured credential was published or exposed in the error", err)
		}
	}
	if _, err := os.Stat(commentPath); !os.IsNotExist(err) {
		t.Fatal("credential rejection invoked gh")
	}
}

func TestExplicitCommentDoesNotReleasePendingAutomaticHead(t *testing.T) {
	s, _, _, _ := testServer(t, "", "")
	item := localReviewItem(t)
	item.Comment = "@mojo review"
	key := item.SessionKey + ":" + item.HeadSHA
	s.pending[key] = true
	s.runOne(context.Background(), item)
	if !s.pending[key] {
		t.Fatal("explicit comment released another queued automatic task")
	}
}

func TestSessionStorageFailureKeepsPublishedResultAndFailedReceipt(t *testing.T) {
	for _, intent := range []github.Intent{github.IntentReview, github.IntentFix} {
		t.Run(string(intent), func(t *testing.T) {
			s, runner, _, commentPath := testServer(t, "", "")
			item := localReviewItem(t)
			item.Intent = intent
			item.HeadBranch = "feature"
			if err := s.store.Put(item.SessionKey, store.Record{Repo: item.Repo, Number: item.Number, SessionID: "original-session"}); err != nil {
				t.Fatal(err)
			}
			storageFailure := func() {
				path := filepath.Join(s.cfg.DataDir, "reviews.json")
				if err := os.Rename(path, path+".before-failure"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			runner.onReview, runner.onFix = storageFailure, storageFailure
			runner.fixEnabled = true
			runner.reviewResult = &agent.ReviewResult{SessionID: "new-session", Text: (&review.Notes{}).Render(), Noted: true}
			url := "https://example.test/acme/web/pull/7#issuecomment-1"
			t.Setenv("MOJO_TEST_COMMENT_URL", url)
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			s.runOne(context.Background(), item)
			if _, err := os.Stat(commentPath); err != nil {
				t.Fatal("storage failure discarded the available result", err)
			}
			if !strings.Contains(logs.String(), "会话失败") || !strings.Contains(logs.String(), "level=ERROR") {
				t.Fatal("session save failure was silent", logs.String())
			}
			rec, _ := s.store.Get(item.SessionKey)
			if rec.SessionID != "original-session" {
				t.Fatal("failed save changed in-memory session", rec.SessionID)
			}
			raw, err := os.ReadFile(filepath.Join(s.cfg.DataDir, "activity.json"))
			if err != nil {
				t.Fatal(err)
			}
			var rows []store.Activity
			if err := json.Unmarshal(raw, &rows); err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1 || rows[0].State != "failed" || rows[0].Stage != "storage" || rows[0].CommentURL != url {
				t.Fatalf("storage failure was hidden by publication success: %s", raw)
			}
		})
	}
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
