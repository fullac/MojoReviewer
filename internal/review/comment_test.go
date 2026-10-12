package review

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPostCommentSetsHostOnlyWhenConfigured(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "comment.log")
	gh := filepath.Join(dir, "gh")
	script := `#!/bin/sh
printf 'GH_HOST=%s\n' "$GH_HOST" >> "$MOJO_TEST_COMMENT"
printf '%s\n' "$@" >> "$MOJO_TEST_COMMENT"
`
	if err := os.WriteFile(gh, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOJO_TEST_COMMENT", logPath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	t.Run("with host", func(t *testing.T) {
		_ = os.Remove(logPath)
		if err := PostComment("acme/web", 7, "审查完成", "ghe.example.com"); err != nil {
			t.Fatal(err)
		}
		body, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		got := string(body)
		if !strings.Contains(got, "GH_HOST=ghe.example.com\n") {
			t.Fatalf("应设置 GH_HOST: %s", got)
		}
		if !strings.Contains(got, "pr\ncomment\n7\n--repo\nacme/web\n--body\n"+BotPrefix+"\n\n审查完成\n") {
			t.Fatalf("评论参数不对: %s", got)
		}
	})

	t.Run("empty host leaves env alone", func(t *testing.T) {
		_ = os.Remove(logPath)
		t.Setenv("GH_HOST", "already.configured")
		if err := PostComment("acme/web", 8, BotPrefix+"\n\n失败原因", ""); err != nil {
			t.Fatal(err)
		}
		body, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		got := string(body)
		// 未写入 GH_HOST= 时，子进程继承 already.configured
		if !strings.HasPrefix(got, "GH_HOST=already.configured\n") {
			t.Fatalf("空 host 不应覆盖已有 GH_HOST: %s", got)
		}
		if strings.Contains(got, "GH_HOST=\n") {
			t.Fatalf("空 host 写了空 GH_HOST: %s", got)
		}
		if !strings.Contains(got, "pr\ncomment\n8\n--repo\nacme/web\n") {
			t.Fatalf("评论参数不对: %s", got)
		}
	})
}
