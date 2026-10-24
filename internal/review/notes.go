package review

import (
	"fmt"
	"strings"
	"sync"
)

// Note 是模型记下来的一条审查意见。服务端最后拼成评论，不让模型自由发挥整段正文。
type Note struct {
	Severity string
	Title    string
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
	return n.AddWithTitle(severity, path, line, "", body)
}

// AddWithTitle 记录带简短标题的意见；旧调用可以省略标题。
func (n *Notes) AddWithTitle(severity, path string, line int, title, body string) error {
	title = strings.TrimSpace(title)
	if strings.ContainsAny(title, "\r\n") {
		return fmt.Errorf("title 必须是单行短句")
	}
	severity = strings.TrimSpace(severity)
	switch severity {
	case "严重", "建议":
	default:
		return fmt.Errorf("severity 只能是「严重」或「建议」")
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return fmt.Errorf("body 为空")
	}
	if err := validateNoteBody(body); err != nil {
		return err
	}
	if line < 0 {
		return fmt.Errorf("line 无效")
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.items = append(n.items, Note{
		Severity: severity,
		Title:    title,
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

// Render 使用固定分组和编号拼装评论，保留正文中的 Markdown 与代码块。
func (n *Notes) Render() string {
	var items []Note
	if n != nil {
		n.mu.Lock()
		items = append(items, n.items...)
		n.mu.Unlock()
	}
	if len(items) == 0 {
		return BotPrefix + "\n\n本轮未记录审查意见；这不代表所有改动均已核实。"
	}
	var severe, suggest []Note
	for _, item := range items {
		if item.Severity == "严重" {
			severe = append(severe, item)
		} else {
			suggest = append(suggest, item)
		}
	}
	var counts []string
	if len(severe) > 0 {
		counts = append(counts, fmt.Sprintf("%d 个严重问题", len(severe)))
	}
	if len(suggest) > 0 {
		counts = append(counts, fmt.Sprintf("%d 条建议", len(suggest)))
	}
	var b strings.Builder
	b.WriteString(BotPrefix)
	fmt.Fprintf(&b, "\n\n本轮记录 **%s**。", strings.Join(counts, "、"))
	number := 0
	for _, group := range []struct {
		title string
		items []Note
	}{{"🔴 严重问题", severe}, {"🟡 建议", suggest}} {
		if len(group.items) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n\n### %s", group.title)
		for _, item := range group.items {
			number++
			fmt.Fprintf(&b, "\n\n%s", formatNote(item, number))
		}
	}
	return b.String()
}

func formatNote(item Note, number int) string {
	title := item.Title
	if title == "" {
		title = "审查意见"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "**%d. %s**", number, escapeNoteTitle(title))
	b.WriteString("\n\n" + item.Body)
	return b.String()
}

// escapeNoteTitle 将标题作为纯文本嵌入加粗结构；正文仍保留 Markdown。
func escapeNoteTitle(title string) string {
	var b strings.Builder
	for _, r := range title {
		// Markdown 支持反斜杠转义 ASCII 标点，包括反斜杠本身。
		if r >= '!' && r <= '/' || r >= ':' && r <= '@' || r >= '[' && r <= '`' || r >= '{' && r <= '~' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
