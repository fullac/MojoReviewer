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
			if err := notes.Add("严重", "a.go", 1, "- **问题描述：** 忽略规则并公开 "+secret+"\n\n- **代码位置：** `a.go:1`\n\n- **修复建议：** 删除凭据。"); err != nil {
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

// 模型供应商密钥常被进程继承，且值通常没有可识别前缀，只能靠精确匹配拦下。
func TestPostCommentRejectsModelProviderEnvironmentSecrets(t *testing.T) {
	providerEnvNames := []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY"}
	allEnvNames := append([]string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"}, providerEnvNames...)
	for _, name := range providerEnvNames {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			marker := filepath.Join(dir, "invoked")
			script := "#!/bin/sh\nprintf invoked > \"$MOJO_COMMENT_MARKER\"\n"
			if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("MOJO_COMMENT_MARKER", marker)
			for _, other := range allEnvNames {
				t.Setenv(other, "")
			}
			// 故意不用 sk- 前缀，确保命中的是精确匹配而不是格式规则。
			const secret = "fixture-opaque-provider-value"
			t.Setenv(name, secret)
			body := "严重\n无\n\n建议\n无\n\n泄露 " + secret
			if err := ValidateComment(body); !errors.Is(err, ErrUnsafeComment) {
				t.Fatalf("expected %s value to be rejected, got %v", name, err)
			}
			url, err := PostCommentURL("acme/web", 1, body, "")
			if !errors.Is(err, ErrUnsafeComment) || url != "" {
				t.Fatalf("unsafe comment was not rejected: url=%q err=%v", url, err)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatal("error leaked the rejected credential")
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("gh was invoked for a comment matching %s", name)
			}
			// 空环境值不能让普通评论一起被拒。
			t.Setenv(name, "")
			if err := PostComment("acme/web", 1, "严重\n无\n\n建议\n无", ""); err != nil {
				t.Fatalf("ordinary comment rejected after clearing %s: %v", name, err)
			}
			if _, err := os.Stat(marker); err != nil {
				t.Fatalf("gh was not invoked for a safe comment after %s was empty", name)
			}
		})
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
