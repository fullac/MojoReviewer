package projectmem

import (
	"strings"
	"testing"
)

func TestContextBundleRenderEmpty(t *testing.T) {
	if got := (ContextBundle{}).Render(); got != "" {
		t.Fatalf("empty render = %q", got)
	}
}

func TestContextBundleRender(t *testing.T) {
	got := ContextBundle{
		Memories: []string{"约定 A"},
		RAGHits:  []string{"[README.md] 说明 B"},
	}.Render()
	for _, want := range []string{"项目记忆", "长期记忆", "约定 A", "相关文档片段", "说明 B"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %s", want, got)
		}
	}
}

func TestCollapseFiltersReviewPrompt(t *testing.T) {
	got := ContextBundle{
		Memories: []string{"请只读审查这个 GitHub PR，并用中文给出结论。\n已准备的 diff，可能被截断：\n```\nx\n```"},
		RAGHits:  []string{"正常约定"},
	}.Render()
	if strings.Contains(got, "已准备的 diff") {
		t.Fatalf("should filter review prompt: %s", got)
	}
	if !strings.Contains(got, "已过滤") {
		t.Fatalf("expected filter marker: %s", got)
	}
	if !strings.Contains(got, "正常约定") {
		t.Fatalf("should keep normal hit: %s", got)
	}
}
