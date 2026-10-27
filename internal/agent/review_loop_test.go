// Verify Mojo's embedded execution boundaries without calling an external model.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mojoreviewer/mojoreviewer/internal/config"
	"github.com/mojoreviewer/mojoreviewer/internal/github"
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
			input := ReviewInput{WorkDir: dir, Review: github.Review{Repo: "acme/web", Number: 7, SessionKey: "acme/web#7"}}
			var result ReviewResult
			if tc.fix {
				result, err = runner.Fix(context.Background(), input)
			} else {
				result, err = runner.Review(context.Background(), input)
			}
			if tc.fix {
				if !errors.Is(err, errRepeatedToolLoop) || result.Text != "" {
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
				resumed, err := runner.Review(context.Background(), input)
				if err != nil || resumed.SessionID != result.SessionID || resumed.Noted || !strings.Contains(resumed.Text, "未记录审查意见") {
					t.Fatalf("session reuse leaked prior notes: %+v %v", resumed, err)
				}
				if verifications.Load() != 0 {
					t.Fatal("session reuse invoked completion verifier")
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
		chunk, _ := json.Marshal(map[string]any{"id": "fixture", "choices": []any{choice}})
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"id": "fixture", "choices": []any{choice}})
}
