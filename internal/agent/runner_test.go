package agent

import (
	"strings"
	"testing"

	"github.com/mojoreviewer/mojoreviewer/internal/github"
	"github.com/yurika0211/luckyagent/sdk"
)

func TestPromptUsesReviewTools(t *testing.T) {
	got := prompt(ReviewInput{
		Review:  github.Review{Repo: "acme/web", Number: 1, BaseBranch: "main", HeadBranch: "fix", HeadSHA: "head"},
		WorkDir: "/tmp/review",
		Diff:    "diff",
	})
	for _, want := range []string{
		"pr_diff_stat",
		"pr_file_diff",
		"read_repo_file",
		"repo_grep",
		"changed_symbols",
		"review_note",
		"tests_around",
		"detached checkout",
		"shallow",
		"refs/review/base",
		"git diff main...HEAD",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("提示缺少 %q:\n%s", want, got)
		}
	}
}

func TestArgHelpers(t *testing.T) {
	args := map[string]any{"path": " a.go ", "line": float64(12), "n": "3"}
	if got := argString(args, "path"); got != "a.go" {
		t.Fatalf("argString = %q", got)
	}
	if got := argInt(args, "line"); got != 12 {
		t.Fatalf("argInt float = %d", got)
	}
	if got := argInt(args, "n"); got != 3 {
		t.Fatalf("argInt string = %d", got)
	}
	if got := argInt(nil, "line"); got != 0 {
		t.Fatalf("argInt nil = %d", got)
	}
}

func TestSetSessionWorkingDir(t *testing.T) {
	home := t.TempDir()
	auto := true
	agent, err := sdk.New(sdk.Config{
		HomeDir:     home,
		Provider:    "openai",
		Model:       "gpt-test",
		APIKey:      "sk-test",
		AutoApprove: &auto,
	})
	if err != nil {
		t.Fatalf("sdk.New: %v", err)
	}
	defer agent.Close()

	sessionID, err := agent.NewSessionWithTitle("cwd-test")
	if err != nil {
		t.Fatalf("NewSessionWithTitle: %v", err)
	}
	dir := t.TempDir()
	if err := setSessionWorkingDir(agent, sessionID, dir); err != nil {
		t.Fatalf("setSessionWorkingDir: %v", err)
	}
	sess, ok := agent.Underlying().Sessions().Get(sessionID)
	if !ok || sess == nil {
		t.Fatal("session missing")
	}
	if got := sess.GetCwd(); got != dir {
		t.Fatalf("cwd = %q, want %q", got, dir)
	}
}

