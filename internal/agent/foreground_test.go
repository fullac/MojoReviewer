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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mojoreviewer/mojoreviewer/internal/config"
	"github.com/mojoreviewer/mojoreviewer/internal/github"
)

// 使用真实 SDK foreground 和本地假模型，验证 SDK 熔断能携带笔记/进度返回宿主。
func TestForegroundRepeatedReadsReturnRecoverableFailure(t *testing.T) {
	var requests atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Stream bool `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", 400)
			return
		}
		call := requests.Add(1)
		name, arguments := "read_repo_file", `{"path":"a.go","start_line":1,"end_line":1}`
		if call == 1 {
			name, arguments = "review_note", `{"severity":"建议","path":"a.go","line":1,"body":"fixture finding before interruption"}`
		}
		tool := map[string]any{
			"id": fmt.Sprintf("call-%d", call), "type": "function",
			"function": map[string]string{"name": name, "arguments": arguments},
		}
		if req.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			tool["index"] = 0
			chunk, _ := json.Marshal(map[string]any{"id": "fixture", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{tool}}, "finish_reason": "tool_calls"}}})
			fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "fixture", "choices": []any{map[string]any{
				"index": 0, "finish_reason": "tool_calls",
				"message": map[string]any{"role": "assistant", "content": "", "tool_calls": []any{tool}},
			}},
		})
	}))
	defer provider.Close()
	runner, err := New(config.AgentConfig{
		HomeDir: t.TempDir(), Provider: "openai-compatible", Model: "test-model",
		APIKey: "test-key", APIBase: provider.URL + "/v1", MaxContextTokens: 80000,
		TaskTimeout: 10 * time.Second, RequestTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := runner.Review(context.Background(), ReviewInput{
		WorkDir: dir, Review: github.Review{Repo: "acme/web", Number: 7, SessionKey: "acme/web#7"},
	})
	var failure *ReviewFailure
	if !errors.As(err, &failure) || !strings.Contains(failure.Reason, "重复读取") || failure.TaskID == "" {
		t.Fatalf("未识别实际 SDK 熔断: %v (requests=%d)", err, requests.Load())
	}
	if !result.Noted || !strings.Contains(result.Text, "fixture finding before interruption") || !strings.Contains(failure.Coverage, `"a.go"`) {
		t.Fatalf("SDK 中断丢失笔记/回执: %+v %+v", result, failure)
	}
	if requests.Load() > 15 {
		t.Fatalf("重复读取未及时收束: %d 次模型请求", requests.Load())
	}
}
