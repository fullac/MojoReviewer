package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ResolveComment 补齐评论事件缺少的 PR 信息，保留触发评论和会话标识。
// host 为空时不传 --hostname，沿用 gh 当前认证的主机。
func ResolveComment(ctx context.Context, item Review, host string) (Review, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	endpoint := fmt.Sprintf("repos/%s/pulls/%d", item.Repo, item.Number)
	args := []string{"api"}
	if host = strings.TrimSpace(host); host != "" {
		args = append(args, "--hostname", host)
	}
	args = append(args, endpoint)
	cmd := exec.CommandContext(ctx, "gh", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	raw, err := cmd.Output()
	if err != nil {
		return Review{}, fmt.Errorf("获取 PR 信息失败: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var pr struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		Body    string `json:"body"`
		HTMLURL string `json:"html_url"`
		User    struct {
			Login string `json:"login"`
		} `json:"user"`
		Base struct {
			ref
			Repo repo `json:"repo"`
		} `json:"base"`
		Head ref `json:"head"`
	}
	if err := json.Unmarshal(raw, &pr); err != nil {
		return Review{}, fmt.Errorf("解析 PR 信息失败: %w", err)
	}
	if pr.Number != item.Number || !strings.EqualFold(pr.Base.Repo.FullName, item.Repo) {
		return Review{}, fmt.Errorf("PR 信息与请求的仓库或编号不一致")
	}
	if strings.TrimSpace(pr.Head.SHA) == "" || strings.TrimSpace(pr.Base.SHA) == "" || strings.TrimSpace(pr.Base.Ref) == "" || strings.TrimSpace(pr.Base.Repo.CloneURL) == "" {
		return Review{}, fmt.Errorf("PR 信息缺少 head SHA、base SHA/ref 或 clone URL")
	}
	item.Title = pr.Title
	item.Body = pr.Body
	item.Author = pr.User.Login
	item.HTMLURL = pr.HTMLURL
	item.BaseBranch = pr.Base.Ref
	item.BaseSHA = pr.Base.SHA
	item.HeadBranch = pr.Head.Ref
	item.HeadSHA = pr.Head.SHA
	item.CloneURL = pr.Base.Repo.CloneURL
	return item, nil
}
