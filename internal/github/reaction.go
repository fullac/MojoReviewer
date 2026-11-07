// Publish a bounded, best-effort start signal without involving the model.
package github

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ReactStarted adds eyes to the PR or its triggering comment. GitHub returns
// success for an existing reaction by the same account, so retries are safe.
func ReactStarted(ctx context.Context, item Review, host string) error {
	endpoint := fmt.Sprintf("repos/%s/issues/%d/reactions", item.Repo, item.Number)
	if item.Comment != "" {
		// GUI retries have no GitHub comment. Never react to a different message.
		if item.CommentID <= 0 {
			return nil
		}
		endpoint = fmt.Sprintf("repos/%s/issues/comments/%d/reactions", item.Repo, item.CommentID)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	args := []string{"api"}
	if host = strings.TrimSpace(host); host != "" {
		args = append(args, "--hostname", host)
	}
	args = append(args, "--method", "POST", endpoint, "--raw-field", "content=eyes")
	cmd := exec.CommandContext(ctx, "gh", args...)
	if err := cmd.Run(); err != nil {
		// Response bodies and stderr are unnecessary for this optional signal.
		return fmt.Errorf("添加开始表情失败: %w", err)
	}
	return nil
}
