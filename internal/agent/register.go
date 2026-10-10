package agent

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/mojoreviewer/mojoreviewer/internal/review"
	"github.com/yurika0211/luckyagent/sdk"
)

// reviewTools 是一次审查挂进模型的只读工具。
// 它们只看本次检出，并把结论收成结构化笔记。
type reviewTools struct {
	scope review.ToolScope
	notes *review.Notes
}

func registerReviewTools(agent *sdk.Agent, scope review.ToolScope, notes *review.Notes) error {
	tools := reviewTools{scope: scope, notes: notes}
	specs := []sdk.ToolSpec{
		{
			Name:         "pr_diff_stat",
			Description:  "返回本次 PR 相对 refs/review/base 的文件列表和增删行数。truncated 为 true 时，提示词里的整份 diff 已被截断，后面的文件不会出现在提示词中。",
			Handler:      tools.diffStat,
			AutoApprove:  true,
			ParallelSafe: true,
		},
		{
			Name:        "pr_file_diff",
			Description: "只返回一个文件相对 refs/review/base 的 patch。用它代替把整份 diff 塞进上下文。",
			Parameters: map[string]sdk.ToolParam{
				"path": {Type: "string", Description: "检出内的相对路径", Required: true},
			},
			Handler:      tools.fileDiff,
			AutoApprove:  true,
			ParallelSafe: true,
		},
		{
			Name:        "read_repo_file",
			Description: "读取本次检出内的文件。路径不能超出检出目录，也不能包含 ..。可选行号范围。",
			Parameters: map[string]sdk.ToolParam{
				"path":       {Type: "string", Description: "检出内的相对路径或该检出内的绝对路径", Required: true},
				"start_line": {Type: "number", Description: "起始行，1-based，默认 1", Required: false},
				"end_line":   {Type: "number", Description: "结束行，包含。省略则读到上限", Required: false},
			},
			Handler:      tools.readFile,
			AutoApprove:  true,
			ParallelSafe: true,
		},
		{
			Name:        "repo_grep",
			Description: "只在本次检出内搜索。不要用 terminal 加 grep 搜仓库外的路径。",
			Parameters: map[string]sdk.ToolParam{
				"pattern": {Type: "string", Description: "正则表达式", Required: true},
				"glob":    {Type: "string", Description: "可选的 git grep glob，例如 *.go", Required: false},
			},
			Handler:      tools.grep,
			AutoApprove:  true,
			ParallelSafe: true,
		},
		{
			Name:         "changed_symbols",
			Description:  "从本次 patch 抽出改动的函数、类型、方法名。先看动了哪些接口，再按需读调用方。Go 用语法解析，其他语言用声明行正则。",
			Handler:      tools.symbols,
			AutoApprove:  true,
			ParallelSafe: true,
		},
		{
			Name:        "review_note",
			Description: "记录一条审查意见。severity 只能是「严重」或「建议」。服务端会把笔记拼成最终 PR 评论，不要在最终回复里另写一份完整评论。",
			Parameters: map[string]sdk.ToolParam{
				"severity": {Type: "string", Description: "严重 或 建议", Required: true},
				"path":     {Type: "string", Description: "相关文件的相对路径，可空", Required: false},
				"line":     {Type: "number", Description: "相关行号，可空", Required: false},
				"body":     {Type: "string", Description: "这一条意见的正文", Required: true},
			},
			Handler:     tools.note,
			AutoApprove: true,
		},
		{
			Name:        "tests_around",
			Description: "列出改动文件附近已存在的测试文件，包括同名测试、同目录和父目录的测试。path 可空，空则扫描本次所有改动文件。",
			Parameters: map[string]sdk.ToolParam{
				"path": {Type: "string", Description: "可选，某个改动文件的相对路径", Required: false},
			},
			Handler:      tools.tests,
			AutoApprove:  true,
			ParallelSafe: true,
		},
	}
	for _, spec := range specs {
		// 每次审查都重新绑定到当前检出。先卸掉上一次留下的同名工具。
		_ = agent.UnregisterTool(spec.Name)
		if err := agent.RegisterTool(spec); err != nil {
			return fmt.Errorf("注册 %s: %w", spec.Name, err)
		}
	}
	return nil
}

func (t reviewTools) diffStat(_ map[string]any) (string, error) {
	return t.scope.Stat()
}

func (t reviewTools) fileDiff(args map[string]any) (string, error) {
	return t.scope.FileDiff(argString(args, "path"))
}

func (t reviewTools) readFile(args map[string]any) (string, error) {
	return t.scope.ReadFile(argString(args, "path"), argInt(args, "start_line"), argInt(args, "end_line"))
}

func (t reviewTools) grep(args map[string]any) (string, error) {
	return t.scope.Grep(argString(args, "pattern"), argString(args, "glob"))
}

func (t reviewTools) symbols(_ map[string]any) (string, error) {
	return t.scope.ChangedSymbols()
}

func (t reviewTools) note(args map[string]any) (string, error) {
	if err := t.notes.Add(argString(args, "severity"), argString(args, "path"), argInt(args, "line"), argString(args, "body")); err != nil {
		return "", err
	}
	return fmt.Sprintf("已记录，当前 %d 条", t.notes.Len()), nil
}

func (t reviewTools) tests(args map[string]any) (string, error) {
	return t.scope.TestsAround(argString(args, "path"))
}

func argString(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	switch v := args[key].(type) {
	case string:
		return strings.TrimSpace(v)
	case nil:
		return ""
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func argInt(args map[string]any, key string) int {
	if args == nil {
		return 0
	}
	switch v := args[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(v))
		return n
	default:
		return 0
	}
}
