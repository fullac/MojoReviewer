package review

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// PostComment 检查评论中的凭据后用 gh 发到 PR，正文固定带 BotPrefix。
// host 非空时写入 GH_HOST，和 ResolveComment 使用同一个 GitHub 主机。
func PostComment(repo string, number int, body, host string, secrets ...string) error {
	_, err := PostCommentURL(repo, number, body, host, secrets...)
	return err
}

// PostCommentURL returns gh's publication receipt after a successful command.
func PostCommentURL(repo string, number int, body, host string, secrets ...string) (string, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return "", fmt.Errorf("评论为空")
	}
	if err := ValidateComment(body, secrets...); err != nil {
		return "", err
	}
	if !strings.HasPrefix(body, BotPrefix) {
		body = BotPrefix + "\n\n" + body
	}
	cmd := exec.Command("gh", "pr", "comment", fmt.Sprint(number), "--repo", repo, "--body", body)
	// host 为空时不要写 GH_HOST=，否则会盖掉环境里已有的默认主机。
	if host = strings.TrimSpace(host); host != "" {
		cmd.Env = append(os.Environ(), "GH_HOST="+host)
	}
	var stderr, stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("gh pr comment 失败: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// BotPrefix 与 github.BotPrefix 保持一致。放在 review 包是为了让发布评论不依赖 webhook 包。
const BotPrefix = "【MOJORviewer🥕】"
