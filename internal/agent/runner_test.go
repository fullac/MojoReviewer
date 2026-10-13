package agent

import (
	"strings"
	"testing"

	"github.com/mojoreviewer/mojoreviewer/internal/github"
	"github.com/mojoreviewer/mojoreviewer/internal/projectmem"
	"github.com/mojoreviewer/mojoreviewer/internal/review"
	"github.com/yurika0211/luckyagent/sdk"
)

func TestPromptUsesReviewTools(t *testing.T) {
	got := prompt(ReviewInput{
		Review:  github.Review{Repo: "acme/web", Number: 1, BaseBranch: "main", HeadBranch: "fix", HeadSHA: "head"},
		WorkDir: "/tmp/review",
		Diff:    "diff",
		Memory: projectmem.ContextBundle{
			Memories: []string{"仓库约定：比较基线用 refs/review/base"},
			RAGHits:  []string{"[AGENTS.md] 只审查，不改仓库"},
		},
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
		"项目记忆",
		"refs/review/base",
		"只审查，不改仓库",
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


func TestLockToolsToReviewOnly(t *testing.T) {
	home := t.TempDir()
	auto := true
	agent, err := sdk.New(sdk.Config{
		HomeDir:      home,
		Provider:     "openai",
		Model:        "gpt-test",
		APIKey:       "sk-test",
		SystemPrompt: "test",
		AutoApprove:  &auto,
		DisableTools: bootstrapDisableTools,
	})
	if err != nil {
		t.Fatalf("sdk.New: %v", err)
	}
	defer agent.Close()

	notes := &review.Notes{}
	scope := review.ToolScope{Dir: t.TempDir()}
	if err := registerReviewTools(agent, scope, notes); err != nil {
		t.Fatalf("registerReviewTools: %v", err)
	}
	if err := lockToolsToReviewOnly(agent); err != nil {
		t.Fatalf("lockToolsToReviewOnly: %v", err)
	}

	enabled := map[string]bool{}
	for _, info := range agent.ListTools() {
		if info.Enabled {
			enabled[info.Name] = true
		}
	}
	for name := range reviewToolAllowlist {
		if !enabled[name] {
			t.Fatalf("expected review tool enabled: %s", name)
		}
		delete(enabled, name)
	}
	if len(enabled) != 0 {
		names := make([]string, 0, len(enabled))
		for name := range enabled {
			names = append(names, name)
		}
		t.Fatalf("unexpected enabled tools: %v", names)
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
