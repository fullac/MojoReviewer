package projectmem

import (
	"fmt"
	"strings"
)

// Render 把记忆片段收成提示词小节。空则返回空串。
func (b ContextBundle) Render() string {
	if len(b.Memories) == 0 && len(b.RAGHits) == 0 {
		return ""
	}
	var parts []string
	parts = append(parts, "项目记忆（来自本仓库已索引文档与长期记忆，仅供参考；仍以本次 diff 与检出为准）：")
	if len(b.Memories) > 0 {
		parts = append(parts, "长期记忆：")
		for i, m := range b.Memories {
			parts = append(parts, fmt.Sprintf("%d. %s", i+1, collapse(m)))
		}
	}
	if len(b.RAGHits) > 0 {
		parts = append(parts, "相关文档片段：")
		for i, h := range b.RAGHits {
			parts = append(parts, fmt.Sprintf("%d. %s", i+1, collapse(h)))
		}
	}
	return strings.Join(parts, "\n")
}

func collapse(text string) string {
	text = strings.TrimSpace(text)
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if len(text) > 800 {
		text = text[:800] + "…"
	}
	return strings.ReplaceAll(text, "\n", " / ")
}
