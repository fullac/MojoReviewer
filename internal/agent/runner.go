package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mojoreviewer/mojoreviewer/internal/config"
	"github.com/mojoreviewer/mojoreviewer/internal/github"
	"github.com/mojoreviewer/mojoreviewer/internal/review"
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
// Text 优先使用 review_note 拼出的评论；模型没有记笔记时才回退到自由文本。
type ReviewResult struct {
	SessionID string
	Text      string
	Noted     bool
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
	if err := r.agent.SetSessionWorkingDir(sessionID, input.WorkDir); err != nil {
		return ReviewResult{SessionID: sessionID}, fmt.Errorf("设置审查工作目录: %w", err)
	}
	notes := &review.Notes{}
	if err := registerReviewTools(r.agent, review.ToolScope{Dir: input.WorkDir}, notes); err != nil {
		return ReviewResult{SessionID: sessionID}, err
	}
	text, err := r.agent.ChatSession(ctx, sessionID, prompt(input))
	if err != nil {
		return ReviewResult{SessionID: sessionID}, err
	}
	result := ReviewResult{SessionID: sessionID, Text: strings.TrimSpace(text)}
	if notes.Len() > 0 {
		result.Text = notes.Render()
		result.Noted = true
	}
	return result, nil
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
	fmt.Fprintf(&b, "\n已准备的 diff，可能被截断：\n```\n%s\n```\n\n", input.Diff)
	b.WriteString("先用这些工具，不要再把整份仓库读进上下文：\n")
	b.WriteString("1. pr_diff_stat 看文件地图。truncated 为 true 时，用 pr_file_diff 按文件补读。\n")
	b.WriteString("2. changed_symbols 看动了哪些函数和类型，再用 read_repo_file 或 repo_grep 看调用方。\n")
	b.WriteString("3. tests_around 找改动附近的测试，确认行为有没有被覆盖。\n")
	b.WriteString("4. 每发现一条问题就调用 review_note。severity 只能是「严重」或「建议」。\n\n")
	b.WriteString("规则：\n")
	b.WriteString("1. 路径只能落在这次检出内。工作区是 detached checkout，可能是 shallow。比较基线用 refs/review/base，不要运行 git diff main...HEAD，也不要假定本地存在 base 分支。\n")
	b.WriteString("2. 只审查，不修改文件，不执行 git commit、git push、gh pr create、gh pr merge。\n")
	b.WriteString("3. 最终回复只写一句状态。评论正文由 review_note 汇总，不要在回复里再写一份。没有问题时不要调用 review_note。\n")
	return b.String()
}

const systemPrompt = `# SOUL

你是 MojoReviewer，只负责审查 GitHub Pull Request。

使用中文。只阅读代码和 diff，不修改仓库，不推送，不创建或合并 PR。
优先使用 pr_diff_stat、pr_file_diff、read_repo_file、repo_grep、changed_symbols、tests_around、review_note。
这些工具的范围限定在本次检出内。
`
