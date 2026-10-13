package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mojoreviewer/mojoreviewer/internal/config"
	"github.com/mojoreviewer/mojoreviewer/internal/github"
	"github.com/mojoreviewer/mojoreviewer/internal/projectmem"
	"github.com/mojoreviewer/mojoreviewer/internal/review"
	"github.com/yurika0211/luckyagent/sdk"
)

// Runner 在进程内跑一个 LuckyAgent，全局只用一个模型。
type Runner struct {
	agent   *sdk.Agent
	timeout time.Duration
	memory  *projectmem.Indexer
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
		// Bootstrap still registers LuckyAgent builtins. We switch to an allow-list
		// of review tools after registerReviewTools runs (see lockToolsToReviewOnly).
		DisableTools: bootstrapDisableTools,
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

// AttachProjectMemory 挂上项目记忆索引器。可在 New 之后调用。
func (r *Runner) AttachProjectMemory(store *projectmem.Store) {
	if r == nil || r.agent == nil || store == nil {
		return
	}
	r.memory = &projectmem.Indexer{Agent: r.agent, Store: store}
}

// ProjectMemory 返回已挂载的索引器，供测试或诊断。
func (r *Runner) ProjectMemory() *projectmem.Indexer {
	if r == nil {
		return nil
	}
	return r.memory
}

// ReviewInput 是一次只读审查需要的事实。
type ReviewInput struct {
	Review    github.Review
	WorkDir   string
	Diff      string
	SessionID string
	Memory    projectmem.ContextBundle
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
	if err := setSessionWorkingDir(r.agent, sessionID, input.WorkDir); err != nil {
		return ReviewResult{SessionID: sessionID}, fmt.Errorf("设置审查工作目录: %w", err)
	}
	if r.memory != nil {
		r.memory.Ensure(ctx, input.Review.Repo, input.WorkDir, input.Review.HeadSHA)
		input.Memory = r.memory.LoadContext(ctx, input.Review.Repo, input.Review.Title, input.Review.Body, input.Review.Comment)
	}
	notes := &review.Notes{}
	if err := registerReviewTools(r.agent, review.ToolScope{Dir: input.WorkDir}, notes); err != nil {
		return ReviewResult{SessionID: sessionID}, err
	}
	if err := lockToolsToReviewOnly(r.agent); err != nil {
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


// bootstrapDisableTools is the minimum deny-list applied at sdk.New.
// The real restriction is lockToolsToReviewOnly after review tools are registered.
var bootstrapDisableTools = []string{
	"terminal",
	"web_fetch", "web_search", "http_request", "opencli",
	"file_write", "file_patch", "file_delete", "file_move", "file_mkdir",
	"file_read", "file_list", "document_read",
	"computer_act", "computer_observe",
	"rag_index", "rag_search",
	"remember", "recall", "memory_hygiene",
	"delegate_task", "delegate_cancel", "list_tasks", "task_status", "wait_for_tasks",
	"autonomy", "cron",
	"image_generate", "text_to_speech",
}

// reviewToolAllowlist is the only tool set the model may call during a review.
// Keep in sync with registerReviewTools.
var reviewToolAllowlist = map[string]struct{}{
	"pr_diff_stat":    {},
	"pr_file_diff":    {},
	"read_repo_file":  {},
	"repo_grep":       {},
	"changed_symbols": {},
	"tests_around":    {},
	"review_note":     {},
}

// lockToolsToReviewOnly disables every registered tool outside the review allow-list.
// Review tools stay enabled; unknown future builtins are denied by default.
func lockToolsToReviewOnly(agent *sdk.Agent) error {
	if agent == nil {
		return fmt.Errorf("agent 为空")
	}
	for _, info := range agent.ListTools() {
		if _, ok := reviewToolAllowlist[info.Name]; ok {
			if !info.Enabled {
				if err := agent.EnableTool(info.Name); err != nil {
					return fmt.Errorf("启用审查工具 %s: %w", info.Name, err)
				}
			}
			continue
		}
		if !info.Enabled {
			continue
		}
		if err := agent.DisableTool(info.Name); err != nil {
			return fmt.Errorf("禁用工具 %s: %w", info.Name, err)
		}
	}
	return nil
}

// setSessionWorkingDir pins the review session cwd to the checkout.
func setSessionWorkingDir(agent *sdk.Agent, sessionID, dir string) error {
	if agent == nil {
		return fmt.Errorf("agent 为空")
	}
	if err := agent.SetSessionWorkingDir(sessionID, dir); err != nil {
		return err
	}
	return nil
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
	if mem := strings.TrimSpace(input.Memory.Render()); mem != "" {
		fmt.Fprintf(&b, "\n%s\n", mem)
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
