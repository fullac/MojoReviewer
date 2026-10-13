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
