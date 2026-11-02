// 使用真实运行时验证历史发现必须读取当前证据后才能标为已解决。
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/mojoreviewer/mojoreviewer/internal/config"
	"github.com/mojoreviewer/mojoreviewer/internal/review"
)

func TestResolvedFindingRequiresRead(t *testing.T) {
	for _, read := range []bool{false, true} {
		t.Run(fmt.Sprint(read), func(t *testing.T) {
			calls := 0
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Stream bool `json:"stream"`
				}
				json.NewDecoder(r.Body).Decode(&req)
				calls++
				name, args := "review_note", `{"severity":"建议","finding_id":"old-id","status":"resolved","path":"a.go","line":1,"title":"已修复","body":"- **问题描述：** 已核实修复。\n\n- **代码位置：** \u0060a.go:1\u0060\n\n- **修复建议：** 无需再改。"}`
				noteCall := 1
				if read {
					noteCall = 2
				}
				if read && calls == 1 {
					name, args = "read_repo_file", `{"path":"a.go","start_line":1,"end_line":1}`
				}
				if calls > noteCall {
					writeLoopResponse(w, req.Stream, "done", nil)
					return
				}
				writeLoopResponse(w, req.Stream, "", map[string]any{"id": fmt.Sprint(calls), "type": "function", "function": map[string]string{"name": name, "arguments": args}})
			}))
			defer model.Close()
			dir := t.TempDir()
			os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0600)
			for _, args := range [][]string{{"init"}, {"config", "user.name", "test"}, {"config", "user.email", "test@example.test"}, {"add", "."}, {"commit", "-m", "base"}, {"update-ref", "refs/worktree/review/merge-base", "HEAD"}} {
				if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
					t.Fatalf("git %s %v", out, err)
				}
			}
			runner, err := New(config.AgentConfig{HomeDir: t.TempDir(), Provider: "openai-compatible", Model: "test", APIKey: "test", APIBase: model.URL + "/v1", TaskTimeout: 5 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			defer runner.Close()
			in := ReviewInput{WorkDir: dir, Previous: &review.History{Findings: []review.Finding{{ID: "old-id", Status: "pending", Note: review.Note{Path: "a.go", Line: 1}}}}}
			result, err := runner.Review(context.Background(), in)
			if err != nil || result.Noted != read {
				t.Fatalf("read=%v result=%+v err=%v", read, result, err)
			}
			if read && result.Notes[0].Status != "resolved" {
				t.Fatal("status lost")
			}
		})
	}
}
