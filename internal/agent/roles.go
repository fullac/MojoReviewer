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
func runReviewRoles(roles []struct{ id, name, work string }, run func(struct{ id, name, work string }) error) {
	var wg sync.WaitGroup
	for _, role := range roles {
		wg.Add(1)
		go func(role struct{ id, name, work string }) {
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

// reviewRoles 是一次审查的固定分工。它们记录在 task 事件里，供进度查看；
// 静态检查结果由宿主提供，模型不执行仓库代码。
var reviewRoles = []struct {
	id   string
	name string
	work string
}{
	{"map", "地图", "分页读取 pr_diff_stat 和 changed_symbols，只记录文件、符号和未覆盖范围。"},
	{"memory", "记忆", "对照项目文档和已注入记忆，不把记忆当成代码已经正确的证据。"},
	{"behavior", "行为", "按地图读取相关 patch 和调用方，记录行为、接口和兼容性变化。"},
	{"tests", "测试", "使用 tests_around 判断改动附近的测试覆盖，默认不执行测试。"},
	{"static", "静态", "只消费宿主提供的静态检查结果，不执行仓库代码。"},
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
