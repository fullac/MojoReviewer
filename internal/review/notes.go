package review

import (
	"fmt"
	"strings"
	"sync"
)

// Note 是模型记下来的一条审查意见。服务端最后拼成评论，不让模型自由发挥整段正文。
type Note struct {
	Severity string
	Source   string
	Path     string
	Line     int
	Body     string
}

// Notes 是一次审查里累积的意见。并发调用时加锁。
type Notes struct {
	mu    sync.Mutex
	items []Note
}

// Add 记录一条意见。severity 只接受「严重」和「建议」。
func (n *Notes) Add(severity, path string, line int, body string) error {
	return n.AddSource("", severity, path, line, body)
}

// AddSource records an evidence note with its review route, such as memory,
// behavior, tests, or static.
func (n *Notes) AddSource(source, severity, path string, line int, body string) error {
	severity = strings.TrimSpace(severity)
	source = strings.TrimSpace(source)
	switch severity {
	case "严重", "建议":
	default:
		return fmt.Errorf("severity 只能是「严重」或「建议」")
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return fmt.Errorf("body 为空")
	}
	if line < 0 {
		return fmt.Errorf("line 无效")
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.items = append(n.items, Note{
		Severity: severity,
		Source:   source,
		Path:     strings.TrimSpace(path),
		Line:     line,
		Body:     body,
	})
	return nil
}

// Len 返回已记录的条数。
func (n *Notes) Len() int {
	if n == nil {
		return 0
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.items)
}

// Render 拼成要发到 PR 的评论。没有意见时两节都写「无」。
func (n *Notes) Render() string {
	var items []Note
	if n != nil {
		n.mu.Lock()
		items = append(items, n.items...)
		n.mu.Unlock()
	}
	var severe, suggest []string
	for _, item := range items {
		line := formatNote(item)
		if item.Severity == "严重" {
			severe = append(severe, line)
			continue
		}
		suggest = append(suggest, line)
	}
	var b strings.Builder
	b.WriteString(BotPrefix)
	b.WriteString("\n\n严重\n")
	b.WriteString(section(severe))
	b.WriteString("\n\n建议\n")
	b.WriteString(section(suggest))
	return b.String()
}

func formatNote(item Note) string {
	where := strings.TrimSpace(item.Path)
	if where != "" && item.Line > 0 {
		where = fmt.Sprintf("%s:%d", where, item.Line)
	}
	prefix := ""
	if item.Source != "" {
		prefix = "[" + item.Source + "] "
	}
	if where == "" {
		return "- " + prefix + item.Body
	}
	return fmt.Sprintf("- %s %s%s", where, prefix, item.Body)
}

func section(items []string) string {
	if len(items) == 0 {
		return "无"
	}
	return strings.Join(items, "\n")
}
