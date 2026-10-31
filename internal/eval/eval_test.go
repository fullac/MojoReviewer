// 验证固定本地提交回放、真实运行时工具调用、标注匹配与失败样本统计。
package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mojoreviewer/mojoreviewer/internal/agent"
	"github.com/mojoreviewer/mojoreviewer/internal/config"
	"github.com/mojoreviewer/mojoreviewer/internal/review"
)

func fixture(t *testing.T) (string, Case) {
	t.Helper()
	root := t.TempDir()
	seed := filepath.Join(root, "seed")
	bare := filepath.Join(root, "repo.git")
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git failed: %v %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if err := os.Mkdir(seed, 0700); err != nil {
		t.Fatal(err)
	}
	git(seed, "init")
	git(seed, "config", "user.name", "fixture")
	git(seed, "config", "user.email", "fixture@example.test")
	os.WriteFile(filepath.Join(seed, "a.go"), []byte("package fixture\n"), 0600)
	git(seed, "add", ".")
	git(seed, "commit", "-m", "base")
	base := git(seed, "rev-parse", "HEAD")
	os.WriteFile(filepath.Join(seed, "a.go"), []byte("package fixture\nfunc Changed() {}\n"), 0600)
	git(seed, "commit", "-am", "head")
	head := git(seed, "rev-parse", "HEAD")
	git(root, "clone", "--bare", seed, bare)
	return root, Case{ID: "sample", Repository: bare, BaseSHA: base, HeadSHA: head, Expected: []Expected{{Path: "a.go", Start: 2, End: 2}}}
}

func TestReplayWithRealRunner(t *testing.T) {
	root, c := fixture(t)
	raw, _ := json.Marshal([]Case{c})
	path := filepath.Join(root, "cases.json")
	os.WriteFile(path, raw, 0600)
	cases, err := LoadCases(path)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Stream bool `json:"stream"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		message := map[string]any{"role": "assistant", "content": "done"}
		finish := "stop"
		if calls.Add(1) == 1 {
			args, _ := json.Marshal(map[string]any{"severity": "建议", "path": "a.go", "line": 2, "body": "- **问题描述：** fixture\n\n- **代码位置：** `a.go:2`\n\n- **修复建议：** 检查错误。"})
			message["tool_calls"] = []any{map[string]any{"index": 0, "id": "one", "type": "function", "function": map[string]string{"name": "review_note", "arguments": string(args)}}}
			message["content"] = ""
			finish = "tool_calls"
		}
		choice := map[string]any{"index": 0, "message": message, "finish_reason": finish}
		if req.Stream {
			delete(choice, "message")
			choice["delta"] = message
		}
		response, _ := json.Marshal(map[string]any{"id": "test", "choices": []any{choice}, "usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 2, "total_tokens": 12}})
		if req.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", response)
		} else {
			w.Header().Set("Content-Type", "application/json")
			w.Write(response)
		}
	}))
	defer model.Close()
	runner, err := agent.New(config.AgentConfig{HomeDir: filepath.Join(root, "runtime"), Provider: "openai-compatible", Model: "test", APIKey: "test", APIBase: model.URL + "/v1", TaskTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()
	report := Run(context.Background(), cases, filepath.Join(root, "run"), runner)
	if report.Completed != 1 || report.Failed != 0 || report.Scores.F1 != 1 {
		t.Fatalf("bad replay: %+v", report)
	}
	m := report.Cases[0].Metrics
	if m.Iterations != 2 || m.Usage == nil || m.Usage.Total != 24 || m.ToolCount != 1 {
		t.Fatalf("missing real metrics: %+v", m)
	}
	if _, ok := m.Stages["publish"]; ok {
		t.Fatal("evaluation must never publish")
	}
	if calls.Load() != 2 {
		t.Fatal("unexpected model requests")
	}
}

func TestLocationMatchingAndDuplicates(t *testing.T) {
	notes := []review.Note{{Path: "a.go", Line: 2}, {Path: "a.go", Line: 3}, {Path: "a.go", Line: 2}, {Path: "other", Line: 1}}
	expected := []Expected{{Path: "a.go", Start: 2, End: 3}, {Path: "a.go", Start: 2, End: 2}}
	s := Match(notes, expected)
	if s.TP != 2 || s.FP != 2 || s.FN != 0 || s.Precision != 0.5 || s.Recall != 1 || s.HallucinationRate != 0.5 {
		t.Fatalf("bad scores %+v", s)
	}
	empty := Match(nil, nil)
	if empty.F1 != 0 || empty.FP != 0 {
		t.Fatal(empty)
	}
	missed := Match(nil, expected)
	if missed.FN != 2 || missed.Recall != 0 {
		t.Fatal(missed)
	}
}

func TestLoadRejectsFloatingRemoteAndUnlabelledCases(t *testing.T) {
	root, c := fixture(t)
	for _, modify := range []func(*Case){func(c *Case) { c.BaseSHA = "main" }, func(c *Case) { c.Repository = "https://example.test/r.git" }, func(c *Case) { c.Expected = nil }, func(c *Case) { c.Expected[0].Path = "../outside" }} {
		bad := c
		bad.Expected = append([]Expected{}, c.Expected...)
		modify(&bad)
		raw, _ := json.Marshal([]Case{bad})
		p := filepath.Join(root, "bad.json")
		os.WriteFile(p, raw, 0600)
		if _, err := LoadCases(p); err == nil {
			t.Fatal("invalid fixture accepted")
		}
	}
}

type failedRunner struct{}

func (failedRunner) Review(context.Context, agent.ReviewInput) (agent.ReviewResult, error) {
	return agent.ReviewResult{}, fmt.Errorf("private request body")
}
func TestFailedCasesExcludedAndRedacted(t *testing.T) {
	root, c := fixture(t)
	report := Run(context.Background(), []Case{c}, filepath.Join(root, "run"), failedRunner{})
	raw, _ := json.Marshal(report)
	if report.Completed != 0 || report.Failed != 1 || report.Cases[0].Scores != nil || strings.Contains(string(raw), "private") {
		t.Fatalf("wrong failure accounting: %s", raw)
	}
}
