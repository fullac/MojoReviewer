// Verify Mojo's embedded execution boundaries without calling an external model.
package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mojoreviewer/mojoreviewer/internal/config"
	"github.com/mojoreviewer/mojoreviewer/internal/github"
	"github.com/mojoreviewer/mojoreviewer/internal/observe"
	"github.com/mojoreviewer/mojoreviewer/internal/review"
	"github.com/mojoreviewer/mojoreviewer/internal/store"
)

func TestManagedReviewLoop(t *testing.T) {
	for _, tc := range []struct {
		name   string
		calls  int32
		reason string
		noted  bool
		fix    bool
	}{
		{name: "notes_then_finish", calls: 2, noted: true},
		{name: "invalid_note_then_correct", calls: 3, noted: true},
		{name: "zero_notes", calls: 1},
		{name: "empty_completion", calls: 1, reason: "模型空回复"},
		{name: "fix_empty_completion", calls: 1, fix: true},
		{name: "repeated_reads", calls: 6, reason: "重复读取", noted: true},
		{name: "iteration_limit", calls: 30, reason: "迭代上限", noted: true},
		{name: "timeout", calls: 2, reason: "超时", noted: true},
		{name: "fix_repeated_reads", calls: 5, fix: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests, verifications atomic.Int32
			var sawReadReceipt atomic.Bool
			var sawTemplateRejection, wrongTemperature atomic.Bool
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Temperature float64 `json:"temperature"`
					Stream      bool    `json:"stream"`
					Messages    []struct {
						Content string `json:"content"`
					} `json:"messages"`
					Tools []struct {
						Function struct {
							Name string `json:"name"`
						} `json:"function"`
					} `json:"tools"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					http.Error(w, "bad request", 400)
					return
				}
				if !tc.fix {
					// Assert the provider payload, not just the SDK registry: prompt
					// intent gating can silently remove enabled tools. Check every
					// request, including later iterations and the reused session.
					var names []string
					for _, tool := range req.Tools {
						names = append(names, tool.Function.Name)
					}
					slices.Sort(names)
					want := []string{"changed_symbols", "pr_diff_stat", "pr_file_diff", "read_repo_file", "repo_grep", "review_note", "tests_around"}
					if !slices.Equal(names, want) {
						t.Errorf("provider tools = %v, want exactly %v", names, want)
					}
				}
				if req.Temperature != 0.5 {
					wrongTemperature.Store(true)
				}
				if len(req.Messages) > 0 && strings.HasPrefix(req.Messages[0].Content, "Review whether") {
					verifications.Add(1)
					// Deliberately invalid criterion for the empty foreground criteria.
					// This request must never occur with host-managed execution.
					writeLoopResponse(w, req.Stream, `{"status":"complete","reason":"done","evidence":["notes"],"criteria":[{"index":1,"satisfied":true,"source":"candidate","evidence":"notes"}]}`, nil)
					return
				}
				for _, m := range req.Messages {
					if strings.Contains(m.Content, "body 模板不符") {
						sawTemplateRejection.Store(true)
					}
					if strings.Contains(m.Content, "本轮已读，未再次读取") {
						sawReadReceipt.Store(true)
					}
				}
				call := requests.Add(1)
				if tc.name == "empty_completion" || tc.name == "fix_empty_completion" {
					writeLoopResponse(w, req.Stream, "I couldn't produce a complete answer this round. Please retry.", nil)
					return
				}
				if tc.name == "notes_then_finish" && call > 2 {
					found := false
					for _, m := range req.Messages {
						if strings.Contains(m.Content, "<untrusted_pr_data>") {
							found = true
						}
						if m.Content == "继续前台任务 tq-999" {
							t.Error("legacy checkpoint resumed")
						}
					}
					if !found {
						t.Error("reused session lost the normal review prompt")
					}
				}
				if tc.name == "timeout" && call > 1 {
					<-r.Context().Done()
					return
				}
				if tc.name == "zero_notes" || (tc.name == "notes_then_finish" && call > 1) || (tc.name == "invalid_note_then_correct" && call > 2) {
					writeLoopResponse(w, req.Stream, "模型自由文本不应进入评论", nil)
					return
				}
				name, args := "read_repo_file", `{"path":"a.go","start_line":1,"end_line":1}`
				if call == 1 || (tc.name == "invalid_note_then_correct" && call == 2) {
					name, args = "review_note", `{"severity":"建议","path":"a.go","line":1,"body":"- **问题描述：** fixture finding\n\n- **代码位置：** \u0060a.go:1\u0060\n\n- **修复建议：** 检查错误。"}`
					if tc.name == "invalid_note_then_correct" && call == 1 {
						args = strings.Replace(args, `检查错误。`, `检查错误。\n\n- **补充说明：** 不得发布。`, 1)
					}
				} else if tc.name == "iteration_limit" {
					args = fmt.Sprintf(`{"path":"a.go","start_line":%d,"end_line":%d}`, call, call)
				}
				tool := map[string]any{"id": fmt.Sprintf("call-%d", call), "type": "function", "function": map[string]string{"name": name, "arguments": args}}
				writeLoopResponse(w, req.Stream, "", tool)
			}))
			defer provider.Close()
			home := t.TempDir()
			timeout := 10 * time.Second
			if tc.name == "timeout" {
				timeout = time.Second
			}
			runner, err := New(config.AgentConfig{
				HomeDir: home, Provider: "openai-compatible", Model: "test-model", APIKey: "test-key", APIBase: provider.URL + "/v1",
				MaxContextTokens: 80000, TaskTimeout: timeout, RequestTimeout: 2 * time.Second, FixEnabled: tc.fix,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer runner.Close()
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package fixture\n"+strings.Repeat("// fixture\n", 40)), 0600); err != nil {
				t.Fatal(err)
			}
			for _, args := range [][]string{{"init"}, {"config", "user.email", "test@example.test"}, {"config", "user.name", "test"}, {"commit", "--allow-empty", "-m", "base"}, {"update-ref", "refs/worktree/review/merge-base", "HEAD"}, {"add", "."}, {"commit", "-m", "head"}} {
				if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
					t.Fatalf("git: %v %s", err, out)
				}
			}
			input := ReviewInput{WorkDir: dir, Review: github.Review{Repo: "acme/web", Number: 7, SessionKey: "acme/web#7"}}
			if tc.name == "notes_then_finish" {
				input.DataDir = t.TempDir()
				input.AttemptID = "github:harness-progress"
				input.Manifest = &review.Manifest{TotalFiles: 1, Complete: true, Files: []review.ChangedFile{{Path: "a.go", Added: 1}}}
			}
			ctx, metrics := observe.Start(context.Background())
			var result ReviewResult
			if tc.fix {
				result, err = runner.Fix(ctx, input)
			} else {
				result, err = runner.Review(ctx, input)
			}
			snapshot := metrics.Snapshot()
			if _, ok := snapshot.Stages["model"]; !ok || snapshot.Iterations == 0 {
				t.Fatalf("missing model metrics: %+v", snapshot)
			}
			if tc.name == "notes_then_finish" && (snapshot.Usage == nil || snapshot.Usage.Total != 24 || snapshot.ToolCount != 1 || snapshot.Tools[0].Name != "review_note") {
				t.Fatalf("wrong model accounting: %+v", snapshot)
			}
			if tc.name == "notes_then_finish" {
				// 合并主线后，记录发现仍须同时产生进度回执与 Harness 统计。
				events, readErr := store.ReadProgress(input.DataDir, input.AttemptID)
				found := false
				for _, event := range events {
					if event.Kind == "note" && strings.Contains(event.Summary, "review_note a.go") {
						found = true
					}
				}
				if readErr != nil || !found {
					t.Fatalf("harness note lost progress event: %+v %v", events, readErr)
				}
			}
			if tc.fix {
				expected := errRepeatedToolLoop
				if tc.name == "fix_empty_completion" {
					expected = errEmptyModelResponse
				}
				if !errors.Is(err, expected) || result.Text != "" {
					t.Fatalf("incomplete fix must fail before publication: %+v %v", result, err)
				}
			} else if tc.reason == "" {
				if err != nil {
					t.Fatalf("review did not finish: %v", err)
				}
				if !tc.noted && !strings.Contains(result.Text, "未记录审查意见") {
					t.Fatalf("missing zero-note disclaimer: %s", result.Text)
				}
			} else {
				var failure *ReviewFailure
				if !errors.As(err, &failure) || !strings.Contains(failure.Reason, tc.reason) || failure.TaskID != "" {
					t.Fatalf("wrong failure: %v (requests=%d)", err, requests.Load())
				}
				if tc.name == "timeout" && !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("timeout cause lost: %v", err)
				}
				if tc.name == "repeated_reads" && (!sawReadReceipt.Load() || !strings.Contains(failure.Coverage, `"a.go"`)) {
					t.Fatalf("duplicate read receipt lost: %+v", failure)
				}
			}
			if result.Noted != tc.noted || (tc.noted && !strings.Contains(result.Text, "fixture finding")) || strings.Contains(result.Text, "模型自由文本") {
				t.Fatalf("wrong host-rendered notes: %+v", result)
			}
			if requests.Load() != tc.calls || verifications.Load() != 0 {
				t.Fatalf("unexpected calls=%d verifications=%d", requests.Load(), verifications.Load())
			}
			if wrongTemperature.Load() {
				t.Fatal("provider request temperature must be 0.5")
			}
			if tc.name == "invalid_note_then_correct" && (!sawTemplateRejection.Load() || strings.Contains(result.Text, "补充说明") || !strings.Contains(result.Text, "1 条建议")) {
				t.Fatal("invalid note was not rejected and corrected before publication")
			}
			if tasks, _ := filepath.Glob(filepath.Join(home, "runtime", "foreground", "*.json")); len(tasks) > 0 {
				t.Fatalf("foreground task created: %d", len(tasks))
			}
			if tc.name == "notes_then_finish" {
				input.SessionID = result.SessionID
				legacyDir := filepath.Join(home, "runtime", "foreground")
				if err := os.MkdirAll(legacyDir, 0700); err != nil {
					t.Fatal(err)
				}
				legacyPath := filepath.Join(legacyDir, fmt.Sprintf("%x.json", sha256.Sum256([]byte(result.SessionID))))
				legacy := []byte(`{"tasks":[{"id":"tq-999","state":"blocked"}]}`)
				if err := os.WriteFile(legacyPath, legacy, 0600); err != nil {
					t.Fatal(err)
				}
				resumed, err := runner.Review(context.Background(), input)
				if err != nil || resumed.SessionID != result.SessionID || resumed.Noted || !strings.Contains(resumed.Text, "未记录审查意见") {
					t.Fatalf("session reuse leaked prior notes: %+v %v", resumed, err)
				}
				if verifications.Load() != 0 {
					t.Fatal("session reuse invoked completion verifier")
				}
				if raw, err := os.ReadFile(legacyPath); err != nil || string(raw) != string(legacy) {
					t.Fatal("legacy checkpoint changed", err)
				}
			}
		})
	}
}

func writeLoopResponse(w http.ResponseWriter, stream bool, text string, tool map[string]any) {
	message := map[string]any{"role": "assistant", "content": text}
	finish := "stop"
	if tool != nil {
		message["tool_calls"] = []any{tool}
		finish = "tool_calls"
	}
	choice := map[string]any{"index": 0, "finish_reason": finish, "message": message}
	if stream {
		w.Header().Set("Content-Type", "text/event-stream")
		if tool != nil {
			tool["index"] = 0
		}
		delete(choice, "message")
		choice["delta"] = message
		chunk, _ := json.Marshal(map[string]any{"id": "fixture", "choices": []any{choice}, "usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 2, "total_tokens": 12}})
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"id": "fixture", "choices": []any{choice}, "usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 2, "total_tokens": 12}})
}

func TestRealRuntimeSerialBatchesAndSameHeadFollowup(t *testing.T) {
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Stream   bool                             `json:"stream"`
			Tools    []json.RawMessage                `json:"tools"`
			Messages []struct{ Role, Content string } `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			http.Error(w, "bad", 400)
			return
		}
		call := calls.Add(1)
		if len(req.Tools) != 7 {
			t.Errorf("tools=%d", len(req.Tools))
		}
		if call <= 2 {
			users := 0
			batchFound := false
			for _, m := range req.Messages {
				if m.Role == "user" {
					users++
					batchFound = strings.Contains(m.Content, fmt.Sprintf(`"index":%d`, call))
				}
			}
			if users != 1 || !batchFound {
				t.Errorf("batch %d did not start with isolated assigned context: users=%d", call, users)
			}
		}
		writeLoopResponse(w, req.Stream, "done", nil)
	}))
	defer provider.Close()
	dir := t.TempDir()
	for i := 0; i < 21; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%02d.go", i)), []byte("package p\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init"}, {"config", "user.name", "test"}, {"config", "user.email", "test@example.test"}, {"commit", "--allow-empty", "-m", "base"}, {"update-ref", "refs/worktree/review/merge-base", "HEAD"}, {"add", "."}, {"commit", "-m", "head"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v %s", err, out)
		}
	}
	runner, err := New(config.AgentConfig{HomeDir: t.TempDir(), Provider: "openai-compatible", Model: "test", APIKey: "test", APIBase: provider.URL + "/v1", TaskTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()
	ctx, metrics := observe.Start(context.Background())
	result, err := runner.Review(ctx, ReviewInput{WorkDir: dir})
	if err != nil || calls.Load() != 2 || len(result.CoveredFiles) != 21 || metrics.Snapshot().BatchesCompleted != 2 {
		t.Fatalf("%+v %v calls=%d", result, err, calls.Load())
	}
	resumed, err := runner.Review(context.Background(), ReviewInput{WorkDir: dir, SessionID: result.SessionID})
	if err != nil || calls.Load() != 3 || resumed.SessionID != result.SessionID || len(resumed.CoveredFiles) != 21 {
		t.Fatalf("followup %+v %v calls=%d", resumed, err, calls.Load())
	}
}
