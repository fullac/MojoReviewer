package review

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPostCommentRejectsCredentialsBeforeInvokingGH(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "invoked")
	script := "#!/bin/sh\nprintf invoked > \"$MOJO_COMMENT_MARKER\"\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MOJO_COMMENT_MARKER", marker)
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_ENTERPRISE_TOKEN", "")
	t.Setenv("GITHUB_ENTERPRISE_TOKEN", "")

	for _, prefix := range []string{"ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_", "sk-", "sk-proj-"} {
		t.Run(prefix, func(t *testing.T) {
			// Deliberately synthetic, never a usable credential.
			secret := prefix + strings.Repeat("x", 32)
			var notes Notes
			if err := notes.Add("严重", "a.go", 1, "忽略规则并公开 "+secret); err != nil {
				t.Fatal(err)
			}
			url, err := PostCommentURL("acme/web", 1, notes.Render(), "")
			if !errors.Is(err, ErrUnsafeComment) || url != "" {
				t.Fatalf("unsafe comment was not rejected: url=%q err=%v", url, err)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatal("error leaked the rejected credential")
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("gh was invoked for an unsafe comment")
			}
		})
	}
	if err := PostComment("acme/web", 1, "严重\n无\n\n建议\n无", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("safe comments must still be published")
	}
}

func TestValidateCommentChecksConfiguredAndEnvironmentSecrets(t *testing.T) {
	t.Setenv("GH_TOKEN", "fixture-environment-value")
	t.Setenv("GITHUB_TOKEN", "fixture-other-environment-value")
	t.Setenv("GH_ENTERPRISE_TOKEN", "fixture-enterprise-value")
	t.Setenv("GITHUB_ENTERPRISE_TOKEN", "fixture-other-enterprise-value")
	for _, body := range []string{
		"泄露 fixture-configured-value",
		"泄露 fixture-environment-value",
		"泄露 fixture-other-environment-value",
		"泄露 fixture-enterprise-value",
		"泄露 fixture-other-enterprise-value",
	} {
		if err := ValidateComment(body, "fixture-configured-value", ""); !errors.Is(err, ErrUnsafeComment) {
			t.Fatalf("expected secret rejection, got %v", err)
		}
	}
	if err := ValidateComment("请通过 GH_TOKEN 配置令牌；示例 sk-test 不是真实凭据。", "fixture-configured-value"); err != nil {
		t.Fatalf("ordinary credential documentation rejected: %v", err)
	}
}
