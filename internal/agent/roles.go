package agent

import (
	"fmt"
	"path/filepath"
	"strings"

	taskstore "github.com/yurika0211/luckyagent-orchestrator/task"
)

// reviewRoles 是一次审查的固定分工。它们记录在 task 事件里，供进度查看。
// 当前 SDK 的工具注册是进程级的，所以这些分工仍由同一个只读会话按顺序完成，
// 不并发绑定同一个检出。
var reviewRoles = []struct {
	id   string
	name string
	work string
}{
	{"map", "地图", "分页读取 pr_diff_stat 和 changed_symbols，只记录文件、符号和未覆盖范围。"},
	{"memory", "记忆", "对照项目文档和已注入记忆，不把记忆当成代码已经正确的证据。"},
	{"behavior", "行为", "按地图读取相关 patch 和调用方，记录行为、接口和兼容性变化。"},
	{"tests", "测试", "使用 tests_around 判断改动附近的测试覆盖，默认不执行测试。"},
}

func recordReviewPlan(dir, attempt, session string) error {
	// 每个会话单独一个目录。FileStore 的锁只保护自己的实例，
	// 并行审查不能共用同一个 tasks 目录。
	root := filepath.Join(dir, "tasks", safeKey(session))
	store, err := taskstore.NewFileStore(root)
	if err != nil {
		return err
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
	if _, err := store.Create(parent); err != nil {
		return err
	}
	if err := bus.Created(parent); err != nil {
		return err
	}
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
		if _, err := store.Create(child); err != nil {
			return err
		}
		if err := bus.Created(child); err != nil {
			return err
		}
	}
	return nil
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
