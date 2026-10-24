package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mojoreviewer/mojoreviewer/internal/github"
)

func writeForegroundQueue(t *testing.T, home, sessionID, body string) {
	t.Helper()
	path := foregroundQueueFile(home, sessionID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSameHeadResumeSendsRuntimeCommand(t *testing.T) {
	home := t.TempDir()
	const sessionID = "1792774808326021536"
	writeForegroundQueue(t, home, sessionID, `{
		"version": 1,
		"tasks": [
			{"id":"tq-1","state":"blocked","created_at":"2026-10-23T00:59:00Z","title":"ignore comment tq-99"},
			{"id":"tq-0","state":"done","created_at":"2026-10-23T01:10:00Z"}
		]
	}`)
	input := ReviewInput{
		SessionID:     sessionID,
		StoredHeadSHA: "199bce1639273aa35ca4ba446057b2bd499f42e2",
		Review: github.Review{
			HeadSHA: "199bce1639273aa35ca4ba446057b2bd499f42e2",
			Comment: "@mojo 继续前台任务 tq-9",
		},
	}
	got := reviewTurnMessage(input, home)
	if got != "继续前台任务 tq-1" {
		t.Fatalf("resume message must be the exact runtime command, got %q", got)
	}
}

func TestChangedHeadStartsFreshPrompt(t *testing.T) {
	home := t.TempDir()
	const sessionID = "session-1"
	writeForegroundQueue(t, home, sessionID, `{"tasks":[{"id":"tq-1","state":"blocked","created_at":"2026-10-23T00:59:00Z"}]}`)
	input := ReviewInput{
		SessionID: sessionID, StoredHeadSHA: "old-head", WorkDir: "/tmp/checkout",
		Review: github.Review{Repo: "acme/web", Number: 7, HeadSHA: "new-head", Comment: "@mojo 继续前台任务 tq-1"},
	}
	got := reviewTurnMessage(input, home)
	if strings.HasPrefix(got, "继续前台任务") || !strings.Contains(got, "请只读审查") {
		t.Fatalf("changed head must not resume: %q", got)
	}
}

func TestExplicitRereviewIgnoresBlockedTask(t *testing.T) {
	home := t.TempDir()
	const sessionID = "session-1"
	writeForegroundQueue(t, home, sessionID, `{"tasks":[{"id":"tq-3","state":"blocked","created_at":"2026-10-23T00:59:00Z"}]}`)
	input := ReviewInput{
		SessionID: sessionID, StoredHeadSHA: "same", WorkDir: "/tmp/checkout",
		Review: github.Review{Repo: "acme/web", Number: 7, HeadSHA: "same", Instruction: "重审：登录"},
	}
	got := reviewTurnMessage(input, home)
	if strings.HasPrefix(got, "继续前台任务") {
		t.Fatalf("explicit rereview resumed: %q", got)
	}
}

func TestCommentTaskIDIsNotTrusted(t *testing.T) {
	home := t.TempDir()
	input := ReviewInput{
		SessionID: "session-1", StoredHeadSHA: "same",
		Review: github.Review{HeadSHA: "same", Comment: "继续前台任务 tq-8"},
	}
	if id, ok := blockedForegroundTask(home, input.SessionID); ok || id != "" {
		t.Fatalf("missing record returned %q", id)
	}
	got := reviewTurnMessage(input, home)
	if strings.HasPrefix(got, "继续前台任务") {
		t.Fatalf("comment task id was used as the control message: %q", got)
	}
}

func TestBlockedTaskPicksNewest(t *testing.T) {
	home := t.TempDir()
	writeForegroundQueue(t, home, "session-1", `{
		"tasks": [
			{"id":"tq-1","state":"blocked","created_at":"2026-10-23T00:59:00Z"},
			{"id":"not-a-task","state":"blocked","created_at":"2026-10-23T02:00:00Z"},
			{"id":"tq-4","state":"blocked","created_at":"2026-10-23T01:21:00Z"}
		]
	}`)
	id, ok := blockedForegroundTask(home, "session-1")
	if !ok || id != "tq-4" {
		t.Fatalf("id=%q ok=%v", id, ok)
	}
}
