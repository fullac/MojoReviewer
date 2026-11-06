package agent

import "testing"

func TestToolSummaryKeepsWhitelistFields(t *testing.T) {
	got := toolSummary("read_repo_file", map[string]any{
		"path":       "agent/loop.go",
		"start_line": 10,
		"end_line":   40,
		"content":    "secret prompt",
	}, "package agent", nil)
	if got != "read_repo_file agent/loop.go 10-40" {
		t.Fatal(got)
	}
	skipped := toolSummary("pr_file_diff", map[string]any{"path": "/tmp/private/a.go"}, "本轮已读，未再次读取", nil)
	if skipped != "pr_file_diff 已读，跳过" {
		t.Fatal(skipped)
	}
	note := toolSummary("review_note", map[string]any{
		"severity": "严重", "path": "a.go", "line": 12, "title": "预算判断失效", "body": "full body",
	}, "已记录", nil)
	if note != "review_note a.go 严重:12 预算判断失效" {
		t.Fatal(note)
	}
}
