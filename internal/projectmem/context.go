package projectmem

import (
	"fmt"
	"strings"
)

// Render 把记忆片段收成提示词小节。空则返回空串。
// 条数与单条长度有硬上限，避免历史审查 prompt 把 durable goal 撑爆（issue #21）。
func (b ContextBundle) Render() string {
	if len(b.Memories) == 0 && len(b.RAGHits) == 0 {
		return ""
	}
	var parts []string
	parts = append(parts, "项目记忆（来自本仓库已索引文档与长期记忆，仅供参考；仍以本次检出与工具读到的 diff 为准）：")
	if len(b.Memories) > 0 {
		parts = append(parts, "长期记忆：")
		for i, m := range b.Memories {
			if i >= maxRenderMemories {
				break
			}
			parts = append(parts, fmt.Sprintf("%d. %s", i+1, collapse(m)))
		}
	}
	if len(b.RAGHits) > 0 {
		parts = append(parts, "相关文档片段：")
		for i, h := range b.RAGHits {
			if i >= maxRenderRAGHits {
				break
			}
			parts = append(parts, fmt.Sprintf("%d. %s", i+1, collapse(h)))
		}
	}
	return strings.Join(parts, "\n")
}

const (
	maxRenderMemories = 3
	maxRenderRAGHits  = 3
	maxMemoryChars    = 400
)

func collapse(text string) string {
	text = strings.TrimSpace(text)
	text = strings.ReplaceAll(text, "\r\n", "\n")
	// Drop obvious self-produced review prompts so they cannot re-enter the goal.
	if looksLikeReviewPrompt(text) {
		return "（已过滤：历史审查 prompt，不注入）"
	}
	if len(text) > maxMemoryChars {
		text = text[:maxMemoryChars] + "…"
	}
	return strings.ReplaceAll(text, "\n", " / ")
}

func looksLikeReviewPrompt(text string) bool {
	if strings.Contains(text, "已准备的 diff") {
		return true
	}
	if strings.Contains(text, "请只读审查这个 GitHub PR") {
		return true
	}
	if strings.Contains(text, "不要期待提示词里有整份 diff") {
		return true
	}
	// Very long multi-section blobs are almost never useful long-term facts.
	if len(text) > 2000 && strings.Count(text, "\n") > 20 {
		return true
	}
	return false
}
