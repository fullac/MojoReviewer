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
	for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"} {
		if secret := os.Getenv(name); secret != "" && strings.Contains(body, secret) {
			return ErrUnsafeComment
		}
	}
	return nil
}
