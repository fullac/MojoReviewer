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
	inputs       []agent.ReviewInput
	fixInputs    []agent.ReviewInput
	remembers    []string
	fixEnabled   bool
	reviewResult *agent.ReviewResult
	reviewErr    error
	onReview     func()
	onFix        func()
}

func (r *recordingRunner) Review(_ context.Context, input agent.ReviewInput) (agent.ReviewResult, error) {
	r.inputs = append(r.inputs, input)
	if r.onReview != nil {
		r.onReview()
	}
	if r.reviewResult != nil {
		return *r.reviewResult, r.reviewErr
	}
	return agent.ReviewResult{SessionID: "existing-session", Text: "审查完成"}, nil
}

func (r *recordingRunner) Fix(_ context.Context, input agent.ReviewInput) (agent.ReviewResult, error) {
	r.fixInputs = append(r.fixInputs, input)
	if r.onFix != nil {
		r.onFix()
	}
	return agent.ReviewResult{SessionID: "fix-session", Text: "已改超时配置"}, nil
}

func (r *recordingRunner) RememberProject(content string) error {
	r.remembers = append(r.remembers, content)
	return nil
}

func (r *recordingRunner) FixEnabled() bool {
	return r.fixEnabled
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
    if [ "$MOJO_TEST_POST_FAILURE" = "1" ]; then exit 1; fi
    printf 'GH_HOST=%s\n' "$GH_HOST" >> "$MOJO_TEST_COMMENT"
    printf '%s\n' "$@" >> "$MOJO_TEST_COMMENT"
    if [ -n "$MOJO_TEST_COMMENT_URL" ]; then printf '%s\n' "$MOJO_TEST_COMMENT_URL"; fi
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
	t.Setenv("MOJO_TEST_POST_FAILURE", "")
	t.Setenv("MOJO_TEST_COMMENT_URL", "")
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
	// issue #21: bulk diff is no longer preloaded into ReviewInput / prompt.
	if strings.TrimSpace(input.Diff) != "" {
		t.Fatalf("不应再预置整份 diff: %q", input.Diff)
	}
	// checkout still has the real change for pr_file_diff / tools to read
	readme, err := os.ReadFile(filepath.Join(input.WorkDir, "README.md"))
	if err != nil || string(readme) != "after\n" {
		t.Fatalf("工作区未反映 head 内容: %s %v", readme, err)
	}
	rec, ok := s.store.Get("acme/web#7")
	if !ok || rec.HeadSHA != head || rec.SessionID != "existing-session" {
		t.Fatalf("审查记录未更新: %+v", rec)
	}
	comment, err := os.ReadFile(commentPath)
	if err != nil || !strings.Contains(string(comment), "GH_HOST=ghe.example.com\npr\ncomment\n7\n--repo\nacme/web\n--body\n【MOJORviewer🥕】\n\n审查完成") {
		t.Fatalf("没有把审查结果发到配置的主机: %s %v", comment, err)
	}
	// 自动 PR 事件已有完整元数据，无需额外查询 GitHub。
	if err := os.Remove(apiPath); err != nil {
		t.Fatal(err)
	}
	item := input.Review
	item.Comment = ""
	item.AttemptID = "" // a new delivery/attempt, not a retry of the original transport
	s.runOne(context.Background(), item)
	if len(runner.inputs) != 2 {
		t.Fatal("完整的 PR 任务未正常审查")
	}
	if _, err := os.Stat(apiPath); !os.IsNotExist(err) {
		t.Fatalf("自动 PR 事件不应补拉元数据: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(s.cfg.DataDir, "activity.json"))
	if err != nil {
		t.Fatal(err)
	}
	var activities []store.Activity
	if err := json.Unmarshal(raw, &activities); err != nil {
		t.Fatal(err)
	}
	if len(activities) != 2 || activities[1].State != "completed" || activities[1].Stage != "published" {
		t.Fatalf("publication not recorded: %s", raw)
	}
	// An explicit request may retry the same head; only publication fails here.
	item.Comment = "@mojo review 再审"
	t.Setenv("MOJO_TEST_POST_FAILURE", "1")
	s.runOne(context.Background(), item)
	raw, _ = os.ReadFile(filepath.Join(s.cfg.DataDir, "activity.json"))
	if err := json.Unmarshal(raw, &activities); err != nil {
		t.Fatal(err)
	}
	if activities[2].State != "failed" || activities[2].Stage != "publish" || activities[2].CommentURL != "" {
		t.Fatalf("failed publication shown as completed: %s", raw)
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

func TestRememberIntentWritesMemoryWithoutCheckout(t *testing.T) {
	s, runner, _, commentPath := testServer(t, `{"number":7,"title":"t","body":"","user":{"login":"a"},"base":{"ref":"main","sha":"base","repo":{"full_name":"acme/web","clone_url":"file:///tmp"}},"head":{"ref":"f","sha":"head"}}`, "")
	body := `{"action":"created","issue":{"number":7,"title":"t","pull_request":{"url":"x"}},"comment":{"body":"@mojo remember 基线用 refs/review/base","user":{"login":"ada"}},"repository":{"full_name":"acme/web"}}`
	mac := hmac.New(sha256.New, []byte(s.cfg.GitHub.WebhookSecret))
	_, _ = mac.Write([]byte(body))
	req := httptest.NewRequest(http.MethodPost, "/webhook/github", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", "issue_comment")
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	w := httptest.NewRecorder()
	s.handleWebhook(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("未入队: %d %s", w.Code, w.Body.String())
	}
	item := <-s.jobs
	if item.Intent != github.IntentRemember {
		t.Fatalf("intent=%s", item.Intent)
	}
	s.runOne(context.Background(), item)
	if len(runner.inputs) != 0 {
		t.Fatal("remember 不应进入审查")
	}
	if len(runner.remembers) != 1 || !strings.Contains(runner.remembers[0], "基线用 refs/review/base") {
		t.Fatalf("未写入记忆: %#v", runner.remembers)
	}
	comment, err := os.ReadFile(commentPath)
	if err != nil || !strings.Contains(string(comment), "已记住") {
		t.Fatalf("未确认 remember: %s %v", comment, err)
	}
}

func TestFixIntentRepliesUnsupported(t *testing.T) {
	s, runner, _, commentPath := testServer(t, `{"number":7,"title":"t","body":"","user":{"login":"a"},"base":{"ref":"main","sha":"base","repo":{"full_name":"acme/web","clone_url":"file:///tmp"}},"head":{"ref":"f","sha":"head"}}`, "")
	body := `{"action":"created","issue":{"number":7,"title":"t","pull_request":{"url":"x"}},"comment":{"body":"@mojo fix 改超时","user":{"login":"ada"}},"repository":{"full_name":"acme/web"}}`
	mac := hmac.New(sha256.New, []byte(s.cfg.GitHub.WebhookSecret))
	_, _ = mac.Write([]byte(body))
	req := httptest.NewRequest(http.MethodPost, "/webhook/github", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", "issue_comment")
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	w := httptest.NewRecorder()
	s.handleWebhook(w, req)
	item := <-s.jobs
	if item.Intent != github.IntentFix {
		t.Fatalf("intent=%s", item.Intent)
	}
	s.runOne(context.Background(), item)
	if len(runner.inputs) != 0 || len(runner.remembers) != 0 {
		t.Fatal("fix 不应审查或写记忆")
	}
	comment, err := os.ReadFile(commentPath)
	if err != nil || !strings.Contains(string(comment), "fix 模式未启用") {
		t.Fatalf("未提示 fix 未开放: %s %v", comment, err)
	}
}

func TestFixIntentRunsWhenEnabled(t *testing.T) {
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
	s, runner, _, commentPath := testServer(t, string(payload), "")
	runner.fixEnabled = true
	body := `{"action":"created","issue":{"number":7,"title":"t","pull_request":{"url":"x"}},"comment":{"body":"@mojo fix 改超时","user":{"login":"ada"}},"repository":{"full_name":"acme/web"}}`
	mac := hmac.New(sha256.New, []byte(s.cfg.GitHub.WebhookSecret))
	_, _ = mac.Write([]byte(body))
	req := httptest.NewRequest(http.MethodPost, "/webhook/github", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", "issue_comment")
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	w := httptest.NewRecorder()
	s.handleWebhook(w, req)
	item := <-s.jobs
	if item.Intent != github.IntentFix {
		t.Fatalf("intent=%s", item.Intent)
	}
	s.runOne(context.Background(), item)
	if len(runner.inputs) != 0 {
		t.Fatal("fix 不应走 Review")
	}
	if len(runner.fixInputs) != 1 {
		t.Fatalf("fix 未调用: %#v", runner.fixInputs)
	}
	if runner.fixInputs[0].Review.Instruction != "改超时" {
		t.Fatalf("instruction=%q", runner.fixInputs[0].Review.Instruction)
	}
	comment, err := os.ReadFile(commentPath)
	if err != nil || !strings.Contains(string(comment), "fix 结果") || !strings.Contains(string(comment), "已改超时配置") {
		t.Fatalf("未发布 fix 结果: %s %v", comment, err)
	}
	if !strings.Contains(string(comment), "未检测到工作区改动") {
		t.Fatalf("应说明未提交: %s", comment)
	}
}

func TestFixAuthorDenied(t *testing.T) {
	s, runner, _, commentPath := testServer(t, `{"number":7,"title":"t","body":"","user":{"login":"a"},"base":{"ref":"main","sha":"base","repo":{"full_name":"acme/web","clone_url":"file:///tmp"}},"head":{"ref":"f","sha":"head"}}`, "")
	runner.fixEnabled = true
	s.cfg.Agent.FixAllowedAuthors = []string{"owner"}
	body := `{"action":"created","issue":{"number":7,"title":"t","pull_request":{"url":"x"}},"comment":{"body":"@mojo fix 改一下","user":{"login":"intruder"}},"repository":{"full_name":"acme/web"}}`
	mac := hmac.New(sha256.New, []byte(s.cfg.GitHub.WebhookSecret))
	_, _ = mac.Write([]byte(body))
	req := httptest.NewRequest(http.MethodPost, "/webhook/github", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", "issue_comment")
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	w := httptest.NewRecorder()
	s.handleWebhook(w, req)
	item := <-s.jobs
	// ResolveComment 会用 mock gh 填 Author；这里直接改 item 跑 runOne 路径
	item.Author = "intruder"
	item.Intent = github.IntentFix
	s.runOne(context.Background(), item)
	if len(runner.fixInputs) != 0 {
		t.Fatal("未授权作者不应进入 Fix")
	}
	comment, err := os.ReadFile(commentPath)
	if err != nil || !strings.Contains(string(comment), "无权触发 fix") {
		t.Fatalf("comment=%s err=%v", comment, err)
	}
}

func TestFixAuthorAllowedHelper(t *testing.T) {
	if !fixAuthorAllowed(nil, "anyone") {
		t.Fatal("空名单应放行")
	}
	if !fixAuthorAllowed([]string{"ada"}, "Ada") {
		t.Fatal("大小写应忽略")
	}
	if !fixAuthorAllowed([]string{"ada"}, "@ada") {
		t.Fatal("@ 前缀应忽略")
	}
	if fixAuthorAllowed([]string{"ada"}, "bob") {
		t.Fatal("未授权应拒绝")
	}
}
