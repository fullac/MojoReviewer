package review

import (
	"errors"
	"os"
	"regexp"
	"strings"
)

// ErrUnsafeComment deliberately contains neither the rejected text nor the secret.
var ErrUnsafeComment = errors.New("评论包含疑似凭据，已拒绝发布")

var credentialPattern = regexp.MustCompile(`(?i)(?:gh[pousr]_[a-z0-9]{20,}|github_pat_[a-z0-9_]{20,}|sk-(?:proj-|svcacct-)?[a-z0-9_-]{16,})`)

// credentialEnvNames 是进程环境里可能携带凭据的变量名。
// 除 GitHub 令牌外，也覆盖进程继承到的模型供应商密钥：这类值常常没有可识别前缀，
// 既匹配不上 credentialPattern，也不等于本次配置传入的密钥。
var credentialEnvNames = []string{
	"GH_TOKEN",
	"GITHUB_TOKEN",
	"GH_ENTERPRISE_TOKEN",
	"GITHUB_ENTERPRISE_TOKEN",
	"OPENAI_API_KEY",
	"ANTHROPIC_API_KEY",
}

// ValidateComment is the final host-side check for all public comment paths.
// Known configured values also cover credentials without a recognizable prefix.
func ValidateComment(body string, secrets ...string) error {
	if credentialPattern.MatchString(body) {
		return ErrUnsafeComment
	}
	for _, secret := range secrets {
		if secret != "" && strings.Contains(body, secret) {
			return ErrUnsafeComment
		}
	}
	for _, name := range credentialEnvNames {
		if secret := os.Getenv(name); secret != "" && strings.Contains(body, secret) {
			return ErrUnsafeComment
		}
	}
	return nil
}
