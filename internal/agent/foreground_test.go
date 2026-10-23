package agent

import (
	"context"
	"encoding/json"
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

// 使用真实 SDK 和本地假模型。只读重复达到上限后，运行时应要求收束，
// 而不是把前台任务标成 blocked。模型继续重复时，宿主仍保留已有笔记。
func TestForegroundRepeatedReadsConvergeWithNotes(t *testing.T) {
	var requests atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Stream   bool `json:"stream"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", 400)
			return
		}
		if len(req.Messages) > 0 && strings.HasPrefix(req.Messages[0].Content, "Review whether") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"verify","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"{\"status\":\"complete\",\"reason\":\"All requested work has recorded evidence\",\"evidence\":[\"The recorded tool result and final answer satisfy the task\"]}"}}]}`))
			return
		}
		call := requests.Add(1)
		name, arguments := "read_repo_file", `{"path":"a.go","start_line":1,"end_line":1}`
		if call == 1 {
			name, arguments = "review_note", `{"severity":"建议","path":"a.go","line":1,"body":"fixture finding before convergence"}`
		}
		if call >= 8 {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "fixture", "choices": []any{map[string]any{
					"index": 0, "finish_reason": "stop",
					"message": map[string]any{"role": "assistant", "content": "模型自由文本不应进入评论"},
				}},
			})
			return
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
	if err != nil {
		t.Fatalf("审查未收束: %v requests=%d", err, requests.Load())
	}
	if !result.Noted || !strings.Contains(result.Text, "fixture finding before convergence") || strings.Contains(result.Text, "模型自由文本") {
		t.Fatalf("收束后未保留宿主笔记: %+v", result)
	}
	if requests.Load() > 15 {
		t.Fatalf("重复读取未及时收束: %d 次模型请求", requests.Load())
	}
}
