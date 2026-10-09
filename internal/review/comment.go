package review

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// PostComment 用 gh 把审查评论发到 PR。评论固定带 BotPrefix，且不包含命令输出原文以外的调用方内容。
func PostComment(repo string, number int, body string) error {
	body = strings.TrimSpace(body)
	if body == "" {
		return fmt.Errorf("评论为空")
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
