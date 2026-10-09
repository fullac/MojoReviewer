package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mojoreviewer/mojoreviewer/internal/config"
	"github.com/mojoreviewer/mojoreviewer/internal/github"
	"github.com/yurika0211/luckyagent/sdk"
)

// Runner 在进程内跑一个 LuckyAgent，全局只用一个模型。
type Runner struct {
	agent   *sdk.Agent
	timeout time.Duration
}

// New 启动嵌入式 LuckyAgent。HomeDir 必须是本服务自己的目录。
func New(cfg config.AgentConfig) (*Runner, error) {
	auto := true
	agent, err := sdk.New(sdk.Config{
		HomeDir:      cfg.HomeDir,
		Provider:     cfg.Provider,
		Model:        cfg.Model,
		APIKey:       cfg.APIKey,
		APIBase:      cfg.APIBase,
		SystemPrompt: systemPrompt,
		AutoApprove:  &auto,
		DisableTools: []string{
			"web_fetch", "web_search", "http_request",
			"file_write", "file_patch", "file_delete", "file_move", "file_mkdir",
			"computer_act", "computer_open", "computer_click", "computer_type",
		},
	})
	if err != nil {
		return nil, err
	}
	return &Runner{agent: agent, timeout: cfg.TaskTimeout}, nil
}

// Close 关闭运行时。
func (r *Runner) Close() error {
	if r == nil || r.agent == nil {
		return nil
	}
	return r.agent.Close()
}

// ReviewInput 是一次只读审查需要的事实。
type ReviewInput struct {
	Review    github.Review
	WorkDir   string
	Diff      string
	SessionID string
}

// ReviewResult 是模型结论和可复用的会话 ID。
type ReviewResult struct {
	SessionID string
	Text      string
}

// Review 对一个 PR 做一轮只读审查。已有 SessionID 时在同一会话里续接。
func (r *Runner) Review(ctx context.Context, input ReviewInput) (ReviewResult, error) {
	if r == nil || r.agent == nil {
		return ReviewResult{}, fmt.Errorf("agent 未启动")
	}
	if strings.TrimSpace(input.WorkDir) == "" {
		return ReviewResult{}, fmt.Errorf("工作目录为空")
	}
	timeout := r.timeout
	if timeout <= 0 {
		timeout = 20 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	sessionID := strings.TrimSpace(input.SessionID)
	var err error
	if sessionID == "" {
		sessionID, err = r.agent.NewSessionWithTitle(input.Review.SessionKey)
		if err != nil {
			return ReviewResult{}, err
		}
	}
	text, err := r.agent.ChatSession(ctx, sessionID, prompt(input))
	if err != nil {
		return ReviewResult{SessionID: sessionID}, err
	}
	return ReviewResult{SessionID: sessionID, Text: strings.TrimSpace(text)}, nil
}

func prompt(input ReviewInput) string {
	var b strings.Builder
	fmt.Fprintf(&b, "请只读审查这个 GitHub PR，并用中文给出结论。\n\n")
	fmt.Fprintf(&b, "仓库绝对路径：%s\n", input.WorkDir)
	fmt.Fprintf(&b, "仓库：%s\nPR：#%d %s\n", input.Review.Repo, input.Review.Number, input.Review.Title)
	fmt.Fprintf(&b, "分支：%s → %s\n提交：%s\n", input.Review.HeadBranch, input.Review.BaseBranch, input.Review.HeadSHA)
	if input.Review.Comment != "" {
		fmt.Fprintf(&b, "\n用户补充要求：\n%s\n", input.Review.Comment)
	}
	fmt.Fprintf(&b, "\n已准备的 diff：\n```\n%s\n```\n\n", input.Diff)
	b.WriteString("规则：\n")
	b.WriteString("1. 所有 terminal 命令必须带 workdir，值就是上面的仓库绝对路径。\n")
	b.WriteString("2. 读文件时使用仓库内的绝对路径。\n")
	b.WriteString("3. 只审查，不修改文件，不执行 git commit、git push、gh pr create、gh pr merge。\n")
	b.WriteString("4. 最后直接输出要发到 PR 的评论正文，以【MojoReviewer】开头。分成「严重」「建议」两节，没有问题就写「无」。\n")
	return b.String()
}

const systemPrompt = `# SOUL

你是 MojoReviewer，只负责审查 GitHub Pull Request。

使用中文。只阅读代码和 diff，不修改仓库，不推送，不创建或合并 PR。
终端命令必须显式使用调用方给出的绝对工作目录。
`
