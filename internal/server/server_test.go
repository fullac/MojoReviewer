package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mojoreviewer/mojoreviewer/internal/agent"
	"github.com/mojoreviewer/mojoreviewer/internal/config"
	"github.com/mojoreviewer/mojoreviewer/internal/github"
	"github.com/mojoreviewer/mojoreviewer/internal/store"
)

type recordingRunner struct {
	inputs []agent.ReviewInput
}

func (r *recordingRunner) Review(_ context.Context, input agent.ReviewInput) (agent.ReviewResult, error) {
	r.inputs = append(r.inputs, input)
	return agent.ReviewResult{SessionID: "existing-session", Text: "审查完成"}, nil
}

func testServer(t *testing.T, response, failure string) (*Server, *recordingRunner, string, string) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"response": response,
		"failure":  failure,
		"gh": `#!/bin/sh
case "$1" in
  api)
    printf '%s\n' "$@" >> "$MOJO_TEST_API"
    if [ -s "$MOJO_TEST_FAILURE" ]; then cat "$MOJO_TEST_FAILURE" >&2; exit 1; fi
    cat "$MOJO_TEST_RESPONSE"
    ;;
  pr)
    printf 'GH_HOST=%s\n' "$GH_HOST" >> "$MOJO_TEST_COMMENT"
    printf '%s\n' "$@" >> "$MOJO_TEST_COMMENT"
    ;;
  *) exit 2 ;;
esac
`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	apiPath, commentPath := filepath.Join(dir, "api"), filepath.Join(dir, "comment")
	t.Setenv("MOJO_TEST_API", apiPath)
	t.Setenv("MOJO_TEST_COMMENT", commentPath)
	t.Setenv("MOJO_TEST_RESPONSE", filepath.Join(dir, "response"))
	t.Setenv("MOJO_TEST_FAILURE", filepath.Join(dir, "failure"))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	st, err := store.Open(filepath.Join(dir, "reviews.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{DataDir: dir}
	cfg.GitHub.TriggerUser = "mojo"
	cfg.GitHub.WebhookSecret = "test-secret"
	runner := &recordingRunner{}
	s := New(cfg, nil, st)
	s.runner = runner
	return s, runner, apiPath, commentPath
}

func queueComment(t *testing.T, s *Server) github.Review {
	t.Helper()
	body := `{"action":"created","issue":{"number":7,"title":"旧标题","pull_request":{"url":"https://api.github.com/repos/acme/web/pulls/7"}},"comment":{"body":"@mojo 再看一下","user":{"login":"commenter"}},"repository":{"full_name":"acme/web"}}`
	mac := hmac.New(sha256.New, []byte(s.cfg.GitHub.WebhookSecret))
	_, _ = mac.Write([]byte(body))
	req := httptest.NewRequest(http.MethodPost, "/webhook/github", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", "issue_comment")
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	w := httptest.NewRecorder()
	s.handleWebhook(w, req)
	if w.Code != http.StatusAccepted || w.Body.String() != `{"status":"queued"}` {
		t.Fatalf("评论未入队: %d %s", w.Code, w.Body.String())
	}
	select {
	case item := <-s.jobs:
		return item
	default:
		t.Fatal("队列为空")
		return github.Review{}
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestCommentReviewChecksOutLatestHeadAndReusesSession(t *testing.T) {
	origin := t.TempDir()
	runGit(t, origin, "init", "-b", "main")
	runGit(t, origin, "config", "user.name", "mojo")
	runGit(t, origin, "config", "user.email", "mojo@example.com")
	if err := os.WriteFile(filepath.Join(origin, "README.md"), []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, origin, "add", "README.md")
	runGit(t, origin, "commit", "-m", "base")
	base := runGit(t, origin, "rev-parse", "HEAD")
	runGit(t, origin, "switch", "-c", "feature")
	if err := os.WriteFile(filepath.Join(origin, "README.md"), []byte("after\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, origin, "commit", "-am", "head")
	head := runGit(t, origin, "rev-parse", "HEAD")
	payload, err := json.Marshal(map[string]any{
		"number": 7, "title": "最新标题", "body": "PR 说明", "user": map[string]string{"login": "author"},
		"base": map[string]any{"ref": "main", "sha": base, "repo": map[string]string{"full_name": "acme/web", "clone_url": "file://" + origin}},
		"head": map[string]string{"ref": "feature", "sha": head},
	})
	if err != nil {
		t.Fatal(err)
	}
	s, runner, apiPath, commentPath := testServer(t, string(payload), "")
	s.cfg.GitHub.Host = "ghe.example.com"
	if err := s.store.Put("acme/web#7", store.Record{Repo: "acme/web", Number: 7, HeadSHA: "old-head", SessionID: "existing-session"}); err != nil {
		t.Fatal(err)
	}
	s.runOne(context.Background(), queueComment(t, s))
	if len(runner.inputs) != 1 {
		t.Fatalf("评论触发未进入审查: %+v", runner.inputs)
	}
	input := runner.inputs[0]
	if input.SessionID != "existing-session" || input.Review.Comment != "@mojo 再看一下" || input.Review.HeadSHA != head || input.Review.BaseSHA != base {
		t.Fatalf("审查上下文错误: %+v", input)
	}
	if runGit(t, input.WorkDir, "rev-parse", "HEAD") != head || runGit(t, input.WorkDir, "rev-parse", "refs/review/base") != base {
		t.Fatal("没有检出最新 head 或正确 base")
	}
	if !strings.Contains(input.Diff, "+after") || !strings.Contains(input.Diff, "-before") {
		t.Fatalf("没有生成真实 PR diff: %s", input.Diff)
	}
	rec, ok := s.store.Get("acme/web#7")
	if !ok || rec.HeadSHA != head || rec.SessionID != "existing-session" {
		t.Fatalf("审查记录未更新: %+v", rec)
	}
	comment, err := os.ReadFile(commentPath)
	if err != nil || !strings.Contains(string(comment), "GH_HOST=ghe.example.com\npr\ncomment\n7\n--repo\nacme/web\n--body\n【MojoReviewer】\n\n审查完成") {
		t.Fatalf("没有把审查结果发到配置的主机: %s %v", comment, err)
	}
	// 自动 PR 事件已有完整元数据，无需额外查询 GitHub。
	if err := os.Remove(apiPath); err != nil {
		t.Fatal(err)
	}
	item := input.Review
	item.Comment = ""
	s.runOne(context.Background(), item)
	if len(runner.inputs) != 2 {
		t.Fatal("完整的 PR 任务未正常审查")
	}
	if _, err := os.Stat(apiPath); !os.IsNotExist(err) {
		t.Fatalf("自动 PR 事件不应补拉元数据: %v", err)
	}
}

func TestCommentReviewReportsSetupFailure(t *testing.T) {
	for _, tt := range []struct{ name, response, failure, want string }{
		{"API error", "", "HTTP 403: forbidden token=ghp_secret", "补齐 PR 信息失败：访问 GitHub 被拒绝，请检查令牌权限"},
		{"incomplete response", `{"number":7,"base":{"repo":{"full_name":"acme/web"}}}`, "", "PR 信息缺少检出所需字段"},
		{"clone error", `{"number":7,"base":{"ref":"main","sha":"base","repo":{"full_name":"acme/web","clone_url":"file:///nonexistent-mojo-test-repo"}},"head":{"sha":"head"}}`, "", "准备仓库失败：内部错误，详情见服务日志"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, runner, _, commentPath := testServer(t, tt.response, tt.failure)
			s.cfg.GitHub.Host = "ghe.example.com"
			s.runOne(context.Background(), queueComment(t, s))
			if len(runner.inputs) != 0 {
				t.Fatal("准备失败后不应调用模型")
			}
			if _, ok := s.store.Get("acme/web#7"); ok {
				t.Fatal("准备失败不应写入已审查记录")
			}
			comment, err := os.ReadFile(commentPath)
			if err != nil || !strings.Contains(string(comment), "GH_HOST=ghe.example.com\n") || !strings.Contains(string(comment), github.BotPrefix) || !strings.Contains(string(comment), tt.want) {
				t.Fatalf("失败原因未回报到配置的主机: %s %v", comment, err)
			}
			body := string(comment)
			for _, leak := range []string{"ghp_secret", "/nonexistent-mojo-test-repo", "HTTP 403", "file://"} {
				if strings.Contains(body, leak) {
					t.Fatalf("公开评论泄露了内部细节 %q: %s", leak, body)
				}
			}
		})
	}
}

func TestPublicFailureRedactsOperationalDetail(t *testing.T) {
	cases := []struct {
		err  string
		want string
	}{
		{"获取 PR 信息失败: exit status 1: HTTP 403 forbidden token=ghp_secret", "访问 GitHub 被拒绝，请检查令牌权限"},
		{"获取 PR 信息失败: exit status 1: authentication failed for https://ghe.internal/acme/web.git token=ghp_secret", "访问 GitHub 被拒绝，请检查令牌权限"},
		{"git clone 失败: status 404: repository not found", "找不到 PR 或仓库"},
		{"git clone 失败: exit status 128: port 40122 sha 4031aabb", "内部错误，详情见服务日志"},
		{"git clone 失败: exit status 128: fatal: unable to access 'https://ghe.internal/acme/web.git': connection refused", "无法连接 GitHub"},
		{"获取 PR 信息失败: context deadline exceeded", "访问 GitHub 超时"},
		{"PR 信息与请求的仓库或编号不一致", "PR 信息与请求的仓库或编号不一致"},
		{"PR 信息缺少 head SHA、base SHA/ref 或 clone URL", "PR 信息缺少检出所需字段"},
		{"解析 PR 信息失败: invalid character", "PR 信息无法解析"},
		{"git clone 失败: repository not found: /home/shiokou/.mojoreviewer/workspaces/acme", "找不到 PR 或仓库"},
		{"git clone 失败: exit status 128: /home/shiokou/.mojoreviewer/workspaces/acme: permission denied", "内部错误，详情见服务日志"},
	}
	for _, tt := range cases {
		got := publicFailure(errors.New(tt.err))
		if got != tt.want {
			t.Fatalf("publicFailure(%q) = %q, want %q", tt.err, got, tt.want)
		}
		if strings.Contains(got, "ghp_") || strings.Contains(got, "/home/") || strings.Contains(got, "ghe.internal") {
			t.Fatalf("归类结果仍含内部细节: %q", got)
		}
	}
}
