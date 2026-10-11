package review

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

var credentialPattern = regexp.MustCompile(`(?:gh[pousr]_[A-Za-z0-9_]+|github_pat_[A-Za-z0-9_]+|sk-[A-Za-z0-9_-]+)`)

// PostComment 用 gh 发回审查评论。含已知凭据或常见凭据格式时拒绝发布。
func PostComment(repo string, number int, body string, secrets ...string) error {
	body = strings.TrimSpace(body)
	if body == "" {
		return fmt.Errorf("评论为空")
	}
	secrets = append(secrets, os.Getenv("GH_TOKEN"), os.Getenv("GITHUB_TOKEN"), os.Getenv("OPENAI_API_KEY"), os.Getenv("ANTHROPIC_API_KEY"))
	if credentialPattern.MatchString(body) {
		return fmt.Errorf("评论可能包含凭据，已拒绝发布")
	}
	for _, secret := range secrets {
		if secret != "" && strings.Contains(body, secret) {
			return fmt.Errorf("评论可能包含凭据，已拒绝发布")
		}
	}
	if !strings.HasPrefix(body, BotPrefix) {
		body = BotPrefix + "\n\n" + body
	}
	cmd := exec.Command("gh", "pr", "comment", fmt.Sprint(number), "--repo", repo, "--body", body)
	cmd.Env = os.Environ()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gh pr comment 失败: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// BotPrefix 与 github.BotPrefix 保持一致。放在 review 包是为了让发布评论不依赖 webhook 包。
const BotPrefix = "【MojoReviewer】"
