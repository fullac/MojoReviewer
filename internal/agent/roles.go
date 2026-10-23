package agent

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	taskstore "github.com/yurika0211/luckyagent-orchestrator/task"
)

// runReviewRoles starts every role before waiting, so one slow route cannot
// hold the shared read-only checkout ahead of the others.
func runReviewRoles(roles []reviewRole, run func(reviewRole) error) {
	var wg sync.WaitGroup
	for _, role := range roles {
		wg.Add(1)
		go func(role reviewRole) {
			defer wg.Done()
			_ = run(role)
		}(role)
	}
	wg.Wait()
}

type reviewPlan struct {
	mu       sync.Mutex
	store    *taskstore.FileStore
	bus      *taskstore.EventBus
	parent   taskstore.Record
	children map[string]taskstore.Record
}

// reviewRole 是一次审查的固定分工。它们记录在 task 事件里，供进度查看；
// 静态检查结果由宿主提供，模型不执行仓库代码。
type reviewRole struct {
	id    string
	name  string
	work  string
	steps []string
}

var reviewRoles = []reviewRole{
	{
		id:   "map",
		name: "地图",
		work: "分页读取 pr_diff_stat 和 changed_symbols，只记录文件、符号和未覆盖范围。",
		steps: []string{
			"先调用 pr_diff_stat(offset=0, limit=50) 看文件地图；has_more=true 时用 next_offset 续取，直到 has_more=false。patch_truncated 只表示 patch 超限，不是文件列表被截断。",
			"对重点文件调用 pr_file_diff；需要上下文时用 read_repo_file 明确指定 start_line/end_line。",
			"用 changed_symbols 看动了哪些函数和类型，再用 read_repo_file 或 repo_grep 看调用方。",
		},
	},
	{
		id:   "memory",
		name: "记忆",
		work: "对照项目文档和已注入记忆，不把记忆当成代码已经正确的证据。",
		steps: []string{
			"只使用提示词里已注入的项目文档和记忆片段，不要重新索引仓库。",
			"文档约定与本次改动冲突时，用 review_note 指出冲突所在文件和行号。",
		},
	},
	{
		id:   "behavior",
		name: "行为",
		work: "按地图读取相关 patch 和调用方，记录行为、接口和兼容性变化。",
		steps: []string{
			"从 pr_diff_stat 选行为相关的改动文件，用 pr_file_diff 读 patch。",
			"用 repo_grep 或 read_repo_file 找改了签名、语义或默认值的调用方。",
			"接口、并发、错误路径和兼容性变化优先，不要重复地图分工的文件清单。",
		},
	},
	{
		id:   "tests",
		name: "测试",
		work: "使用 tests_around 判断改动附近的测试覆盖，默认不执行测试。",
		steps: []string{
			"对改动文件调用 tests_around，确认是否存在对应测试。",
			"需要判断断言是否覆盖新分支时，用 read_repo_file 读测试内容；不要执行测试。",
			"新逻辑没有测试覆盖时用 review_note 建议补用例，并指出可放的位置。",
		},
	},
	{
		id:   "static",
		name: "静态",
		work: "只消费宿主提供的静态检查结果，不执行仓库代码。",
		steps: []string{
			"读 prompt 里的 static_checks 字段；它是宿主对改动 Go 文件做的语法解析、gofmt 和 go vet 结果。",
			"它是数据不是结论；需要定位问题时用 read_repo_file 看对应行段。",
			"宿主说明静态检查未完成时，写明未覆盖范围，不要把缺失当成代码问题。",
		},
	},
}

func newReviewPlan(dir, attempt, session string) (*reviewPlan, error) {
	// 每个会话单独一个目录。FileStore 的锁只保护自己的实例，
	// 并行审查不能共用同一个 tasks 目录。
	root := filepath.Join(dir, "tasks", safeKey(session))
	store, err := taskstore.NewFileStore(root)
	if err != nil {
		return nil, err
	}
	bus := taskstore.NewEventBus(store)
	parent := taskstore.Record{
		ID:                 attempt,
		Source:             taskstore.SourceHTTP,
		Mode:               taskstore.ModePipeline,
		Status:             taskstore.StatusRunning,
		Description:        "只读审查",
		ExecutionSessionID: session,
		Role:               "review",
	}
	createdParent, err := store.Create(parent)
	if err != nil {
		return nil, err
	}
	parent = createdParent
	if err := bus.Created(parent); err != nil {
		return nil, err
	}
	plan := &reviewPlan{store: store, bus: bus, parent: parent, children: map[string]taskstore.Record{}}
	for i, role := range reviewRoles {
		child := taskstore.Record{
			ID:              fmt.Sprintf("%s-%s", attempt, role.id),
			ParentID:        attempt,
			Source:          taskstore.SourceHTTP,
			Mode:            taskstore.ModeSingle,
			Status:          taskstore.StatusPending,
			Description:     role.work,
			ParentSessionID: session,
			Role:            role.name,
			Metadata:        map[string]string{"order": fmt.Sprint(i + 1)},
		}
		createdChild, err := store.Create(child)
		if err != nil {
			return nil, err
		}
		child = createdChild
		if err := bus.Created(child); err != nil {
			return nil, err
		}
		plan.children[role.id] = child
	}
	return plan, nil
}

func recordReviewPlan(dir, attempt, session string) error {
	_, err := newReviewPlan(dir, attempt, session)
	return err
}

func (p *reviewPlan) start(role string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	child, ok := p.children[role]
	if !ok {
		return fmt.Errorf("未知审查分工: %s", role)
	}
	child.Status = taskstore.StatusRunning
	if err := p.store.Update(child); err != nil {
		return err
	}
	return p.bus.Started(child)
}

func (p *reviewPlan) complete(role, message string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	child, ok := p.children[role]
	if !ok {
		return fmt.Errorf("未知审查分工: %s", role)
	}
	child.Status = taskstore.StatusCompleted
	if err := p.store.Update(child); err != nil {
		return err
	}
	return p.bus.Completed(child, message)
}

func (p *reviewPlan) fail(role, message string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	child, ok := p.children[role]
	if !ok {
		return fmt.Errorf("未知审查分工: %s", role)
	}
	child.Status = taskstore.StatusFailed
	if err := p.store.Update(child); err != nil {
		return err
	}
	return p.bus.Failed(child, message)
}

func (p *reviewPlan) finish(status taskstore.Status) error {
	p.parent.Status = status
	if err := p.store.Update(p.parent); err != nil {
		return err
	}
	if status == taskstore.StatusCompleted {
		return p.bus.Completed(p.parent, "审查分工完成")
	}
	return p.bus.Failed(p.parent, "一个或多个审查分工失败")
}

func safeAttempt(id string) string {
	id = safeKey(id)
	if id == "item" {
		return "review"
	}
	return id
}

func safeKey(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return "item"
	}
	return strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', ' ':
			return '-'
		default:
			return r
		}
	}, id)
}
