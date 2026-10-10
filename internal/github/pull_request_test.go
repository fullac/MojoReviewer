package github

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const pullRequestJSON = `{
	"number":7,"title":"最新标题","body":"PR 说明","html_url":"https://github.com/acme/web/pull/7",
	"user":{"login":"author"},
	"base":{"ref":"main","sha":"base-sha","repo":{"full_name":"acme/web","clone_url":"https://github.com/acme/web.git"}},
	"head":{"ref":"feature","sha":"latest-head","repo":{"full_name":"fork/web","clone_url":"https://github.com/fork/web.git"}}
}`

func fakeGH(t *testing.T, response, failure string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range map[string]string{
		"response": response,
		"failure":  failure,
		"gh": "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$MOJO_TEST_ARGS\"\n" +
			"if [ -s \"$MOJO_TEST_FAILURE\" ]; then cat \"$MOJO_TEST_FAILURE\" >&2; exit 1; fi\ncat \"$MOJO_TEST_RESPONSE\"\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	args := filepath.Join(dir, "args")
	t.Setenv("MOJO_TEST_ARGS", args)
	t.Setenv("MOJO_TEST_FAILURE", filepath.Join(dir, "failure"))
	t.Setenv("MOJO_TEST_RESPONSE", filepath.Join(dir, "response"))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return args
}

func TestResolveCommentRefreshesPullRequest(t *testing.T) {
	argsPath := fakeGH(t, pullRequestJSON, "")
	item := Review{Repo: "acme/web", Number: 7, SessionKey: "acme/web#7", Comment: "@mojo 再看一下", HeadSHA: "old-head"}
	got, err := ResolveComment(context.Background(), item)
	if err != nil {
		t.Fatal(err)
	}
	if got.HeadSHA != "latest-head" || got.HeadBranch != "feature" || got.BaseSHA != "base-sha" || got.BaseBranch != "main" || got.CloneURL != "https://github.com/acme/web.git" {
		t.Fatalf("没有补齐最新 PR 检出信息: %+v", got)
	}
	if got.Comment != item.Comment || got.SessionKey != item.SessionKey || got.Repo != item.Repo || got.Number != item.Number {
		t.Fatalf("触发评论或任务标识发生变化: %+v", got)
	}
	if got.Author != "author" || got.Title != "最新标题" || got.Body != "PR 说明" || got.HTMLURL != "https://github.com/acme/web/pull/7" {
		t.Fatalf("PR 描述未更新: %+v", got)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(args) != "api\n--hostname\ngithub.com\nrepos/acme/web/pulls/7\n" {
		t.Fatalf("查询了错误的 PR: %s", args)
	}
}

func TestResolveCommentRejectsUnusableResponse(t *testing.T) {
	tests := []struct {
		name, response, failure, want string
	}{
		{"api failure", "", "HTTP 403: forbidden", "HTTP 403: forbidden"},
		{"invalid JSON", "not json", "", "解析 PR 信息失败"},
		{"wrong PR", strings.Replace(pullRequestJSON, `"number":7`, `"number":8`, 1), "", "不一致"},
		{"wrong repo", strings.Replace(pullRequestJSON, `"full_name":"acme/web"`, `"full_name":"other/web"`, 1), "", "不一致"},
		{"missing head", strings.Replace(pullRequestJSON, `"sha":"latest-head"`, `"sha":""`, 1), "", "缺少"},
		{"missing base", strings.Replace(pullRequestJSON, `"sha":"base-sha"`, `"sha":""`, 1), "", "缺少"},
		{"missing base ref", strings.Replace(pullRequestJSON, `"ref":"main"`, `"ref":""`, 1), "", "缺少"},
		{"missing clone URL", strings.Replace(pullRequestJSON, `"clone_url":"https://github.com/acme/web.git"`, `"clone_url":""`, 1), "", "缺少"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeGH(t, tt.response, tt.failure)
			_, err := ResolveComment(context.Background(), Review{Repo: "acme/web", Number: 7})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("期望错误 %q，得到 %v", tt.want, err)
			}
		})
	}
}

func TestResolveCommentHonorsCancellation(t *testing.T) {
	argsPath := fakeGH(t, pullRequestJSON, "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ResolveComment(ctx, Review{Repo: "acme/web", Number: 7}); err == nil {
		t.Fatal("取消后的任务仍查询了 PR")
	}
	if _, err := os.Stat(argsPath); !os.IsNotExist(err) {
		t.Fatalf("取消后仍启动了 gh: %v", err)
	}
}
