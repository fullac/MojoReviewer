// 验证上游错误仅公开安全分类，并通过真实 SDK 的本地假服务检查永久错误只请求一次。
package agent

import (
	"context"
	"errors"
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

	"github.com/mojoreviewer/mojoreviewer/internal/config"
	"github.com/mojoreviewer/mojoreviewer/internal/github"
	"github.com/mojoreviewer/mojoreviewer/internal/review"
)

func TestUpstreamClassificationIsBoundedAndSafe(t *testing.T) {
	for _, tc := range []struct {
		name, raw, want string
		permanent       bool
	}{
		{"model", `API error 404: {"error":{"type":"model_not_found","message":"private-response-value"}}`, "type=model_not_found", true},
		{"auth", `API error 401: {"error":{"code":"invalid_api_key"}}`, "凭据", true},
		{"permission", `API error 403: {}`, "权限", true},
		{"rate", `API error 429: {"error":{"type":"rate_limit_error"}}`, "限流", false},
		{"unknown type", `API error 500: {"error":{"type":"private-response-value"}}`, "type=upstream_error", false},
		{"malformed", `API error 404: private-response-value`, "HTTP 404", true},
		{"oversized", "API error 404: " + strings.Repeat("x", 17000), "HTTP 404", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cause := errors.New(tc.raw)
			err := classifyUpstream(fmt.Errorf("loop iteration 1: %w", cause), "test-model")
			_, resultErr := reviewResult("session", "", &review.Notes{}, &readProgress{}, err)
			public := ModelFailureLog(resultErr)
			if !strings.Contains(public, tc.want) || !strings.Contains(public, "model=test-model") || strings.Contains(public, "private-response-value") || permanentUpstreamFailure(resultErr) != tc.permanent || !errors.Is(resultErr, cause) {
				t.Fatalf("unexpected classification: %s", public)
			}
		})
	}
	for _, cause := range []error{errors.New("untrusted text API error 404: {}"), context.Canceled, context.DeadlineExceeded} {
		if got := classifyUpstream(cause, "test-model"); got != cause {
			t.Fatal("unrelated error reclassified")
		}
	}
	if safeModelLabel("opaque-config-value", "opaque-config-value") != "已隐藏" || safeModelLabel("unsafe\nname") != "已隐藏" {
		t.Fatal("unsafe model label exposed")
	}
}

func TestPermanentUpstreamFailureStopsRealBatches(t *testing.T) {
	for _, status := range []int{401, 403, 404} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":{"type":"model_not_found","message":"private-response-value"}}`))
			}))
			defer provider.Close()
			runner, err := New(config.AgentConfig{HomeDir: t.TempDir(), Provider: "openai-compatible", Model: "test-model", APIKey: "fixture-key", APIBase: provider.URL + "/v1", MaxContextTokens: 80000, TaskTimeout: 10 * time.Second, RequestTimeout: 2 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			defer runner.Close()
			dir := t.TempDir()
			git := func(args ...string) {
				t.Helper()
				if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
					t.Fatalf("git %v: %s", err, out)
				}
			}
			git("init")
			git("config", "user.email", "test@example.test")
			git("config", "user.name", "test")
			git("commit", "--allow-empty", "-m", "base")
			git("update-ref", "refs/worktree/review/merge-base", "HEAD")
			for i := 0; i < 25; i++ {
				if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("file%02d.go", i)), []byte("package fixture\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			git("add", ".")
			git("commit", "-m", "head")
			result, err := runner.Review(context.Background(), ReviewInput{WorkDir: dir, Review: github.Review{Repo: "acme/web", Number: 7, SessionKey: "acme/web#7"}})
			public := ModelFailureLog(err)
			if calls.Load() != 1 || !permanentUpstreamFailure(err) || len(result.Unreviewed) != 25 || result.Noted || !strings.Contains(public, fmt.Sprintf("HTTP %d", status)) || !strings.Contains(public, "model=test-model") || strings.Contains(public, "private-response-value") {
				t.Fatalf("calls=%d remaining=%d result=%+v failure=%s", calls.Load(), len(result.Unreviewed), result, public)
			}
		})
	}
}

func TestPermanentUpstreamFailurePreservesCollectedNotes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	calls := 0
	note := review.Note{Severity: "建议", Path: "a.go", Line: 1, Body: "- **问题描述：** 已核实的问题。\n\n- **代码位置：** `a.go:1`\n\n- **修复建议：** 修复。"}
	result, err := executeBatches(ctx, ReviewInput{}, []review.Batch{{Files: []string{"a.go"}}, {Files: []string{"b.go"}}}, func(context.Context, ReviewInput) (ReviewResult, error) {
		calls++
		return ReviewResult{Notes: []review.Note{note}}, classifyUpstream(errors.New("API error 404: {}"), "test-model")
	})
	if calls != 1 || !result.Noted || len(result.Notes) != 1 || len(result.Unreviewed) != 2 || !permanentUpstreamFailure(err) {
		t.Fatalf("notes or remaining scope lost: %+v %v", result, err)
	}
}
