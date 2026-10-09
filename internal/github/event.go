package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// Review 是一次需要审查的 PR。
type Review struct {
	Repo       string
	Number     int
	Title      string
	Body       string
	BaseBranch string
	BaseSHA    string
	HeadBranch string
	HeadSHA    string
	CloneURL   string
	Author     string
	HTMLURL    string
	Comment    string
	SessionKey string
}

// ParseWebhook 把 GitHub pull_request 和 issue_comment 转成审查任务。
// 只处理 opened、reopened、synchronize，以及 @触发用户 的 PR 评论。
func ParseWebhook(event string, body []byte, triggerUser string, allowed map[string]bool) (*Review, error) {
	switch event {
	case "pull_request":
		return parsePullRequest(body, allowed)
	case "issue_comment":
		return parseComment(body, triggerUser, allowed)
	default:
		return nil, nil
	}
}

func parsePullRequest(body []byte, allowed map[string]bool) (*Review, error) {
	var payload struct {
		Action      string `json:"action"`
		PullRequest struct {
			Number int    `json:"number"`
			Title  string `json:"title"`
			Body   string `json:"body"`
			Draft  bool   `json:"draft"`
			User   struct {
				Login string `json:"login"`
			} `json:"user"`
			HTMLURL string `json:"html_url"`
			Base    ref    `json:"base"`
			Head    ref    `json:"head"`
		} `json:"pull_request"`
		Repository repo `json:"repository"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("解析 pull_request: %w", err)
	}
	switch payload.Action {
	case "opened", "reopened", "synchronize":
	default:
		return nil, nil
	}
	if payload.PullRequest.Draft {
		return nil, nil
	}
	review, err := newReview(payload.Repository, payload.PullRequest.Number, payload.PullRequest.Title, payload.PullRequest.Body, payload.PullRequest.User.Login, payload.PullRequest.HTMLURL, payload.PullRequest.Base, payload.PullRequest.Head, allowed)
	return review, err
}

func parseComment(body []byte, triggerUser string, allowed map[string]bool) (*Review, error) {
	triggerUser = strings.TrimPrefix(strings.TrimSpace(triggerUser), "@")
	if triggerUser == "" {
		return nil, nil
	}
	var payload struct {
		Action string `json:"action"`
		Issue  struct {
			Number      int    `json:"number"`
			Title       string `json:"title"`
			PullRequest *struct {
				URL string `json:"url"`
			} `json:"pull_request"`
		} `json:"issue"`
		Comment struct {
			Body string `json:"body"`
			User struct {
				Login string `json:"login"`
			} `json:"user"`
		} `json:"comment"`
		Repository repo `json:"repository"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("解析 issue_comment: %w", err)
	}
	if payload.Action != "created" || payload.Issue.PullRequest == nil {
		return nil, nil
	}
	if !mentions(payload.Comment.Body, triggerUser) {
		return nil, nil
	}
	if isBotComment(payload.Comment.Body) {
		return nil, nil
	}
	review, err := newReview(payload.Repository, payload.Issue.Number, payload.Issue.Title, "", payload.Comment.User.Login, "", ref{}, ref{}, allowed)
	if review == nil || err != nil {
		return review, err
	}
	review.Comment = payload.Comment.Body
	return review, nil
}

type repo struct {
	FullName string `json:"full_name"`
	CloneURL string `json:"clone_url"`
}

type ref struct {
	Ref string `json:"ref"`
	SHA string `json:"sha"`
}

func newReview(repository repo, number int, title, body, author, htmlURL string, base, head ref, allowed map[string]bool) (*Review, error) {
	if repository.FullName == "" || number <= 0 {
		return nil, fmt.Errorf("webhook 缺少仓库或 PR 编号")
	}
	if len(allowed) > 0 && !allowed[repository.FullName] {
		return nil, nil
	}
	return &Review{
		Repo:       repository.FullName,
		Number:     number,
		Title:      title,
		Body:       body,
		BaseBranch: base.Ref,
		BaseSHA:    base.SHA,
		HeadBranch: head.Ref,
		HeadSHA:    head.SHA,
		CloneURL:   repository.CloneURL,
		Author:     author,
		HTMLURL:    htmlURL,
		SessionKey: fmt.Sprintf("%s#%d", repository.FullName, number),
	}, nil
}

func mentions(body, user string) bool {
	return strings.Contains(strings.ToLower(body), "@"+strings.ToLower(user))
}

func isBotComment(body string) bool {
	return strings.HasPrefix(strings.TrimSpace(body), BotPrefix)
}

// BotPrefix 是本服务发出的评论前缀，用来避免自己的评论再次触发。
const BotPrefix = "【MojoReviewer】"

// ValidSignature 校验 GitHub HMAC-SHA256 签名。secret 为空时拒绝。
func ValidSignature(secret string, body []byte, header string) bool {
	if secret == "" || !strings.HasPrefix(header, "sha256=") {
		return false
	}
	got, err := hex.DecodeString(strings.TrimPrefix(header, "sha256="))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}
