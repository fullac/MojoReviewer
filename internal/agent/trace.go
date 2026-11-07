package agent

import (
	"fmt"
	"strings"
	"sync"

	"github.com/mojoreviewer/mojoreviewer/internal/store"
)

// progressTrace writes sanitized tool steps for one attempt.
// Tool handlers call it; the file never receives arguments outside the whitelist.
type progressTrace struct {
	dir, id string
	mu      sync.Mutex
	models  int
}

func (t *progressTrace) add(kind, summary string) {
	if t == nil || t.dir == "" || t.id == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	_ = store.AppendProgress(t.dir, t.id, store.ProgressEvent{Kind: kind, Summary: summary})
}

func (t *progressTrace) modelRequest() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.models++
	n := t.models
	t.mu.Unlock()
	t.add("model", fmt.Sprintf("第 %d 次模型请求", n))
}

func toolSummary(name string, args map[string]any, result string, err error) string {
	path := safeRel(argString(args, "path"))
	var b strings.Builder
	b.WriteString(name)
	if path != "" {
		b.WriteString(" ")
		b.WriteString(path)
	}
	switch name {
	case "pr_diff_stat":
		fmt.Fprintf(&b, " offset=%d limit=%d", argInt(args, "offset"), argInt(args, "limit"))
	case "read_repo_file":
		fmt.Fprintf(&b, " %d-%d", argInt(args, "start_line"), argInt(args, "end_line"))
	case "review_note":
		severity := argString(args, "severity")
		if severity != "严重" && severity != "建议" {
			severity = "建议"
		}
		line := argInt(args, "line")
		title := oneLine(argString(args, "title"), 80)
		fmt.Fprintf(&b, " %s", severity)
		if line > 0 {
			fmt.Fprintf(&b, ":%d", line)
		}
		if title != "" {
			b.WriteString(" ")
			b.WriteString(title)
		}
	}
	if err != nil {
		b.WriteString(" 失败")
		return b.String()
	}
	if strings.HasPrefix(result, "本轮已读") {
		b.WriteString(" 已读，跳过")
	}
	return b.String()
}

func safeRel(path string) string {
	path = strings.TrimSpace(strings.ReplaceAll(path, "\\", "/"))
	if path == "" || strings.Contains(path, "..") || strings.HasPrefix(path, "/") {
		return ""
	}
	return path
}

func oneLine(text string, max int) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) > max {
		return string(runes[:max])
	}
	return text
}
