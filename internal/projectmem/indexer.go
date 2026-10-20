package projectmem

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yurika0211/luckyagent-app/sdk"
)

// Indexer 把仓库高信号文档写入 LuckyAgent 隔离家目录里的 RAG / 记忆。
type Indexer struct {
	Agent        *sdk.Agent
	Store        *Store
	MaxFiles     int
	MaxFileBytes int
}

// Ensure 在审查前做增量索引。失败只记日志，不阻断审查。
func (idx *Indexer) Ensure(ctx context.Context, repo, workDir, headSHA string) {
	if idx == nil || idx.Agent == nil || idx.Store == nil {
		return
	}
	repo = strings.TrimSpace(repo)
	workDir = strings.TrimSpace(workDir)
	if repo == "" || workDir == "" {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := idx.ensure(ctx, repo, workDir, headSHA); err != nil {
		log.Printf("项目记忆索引跳过 %s: %v", repo, err)
	}
}

func (idx *Indexer) ensure(ctx context.Context, repo, workDir, headSHA string) error {
	cands, err := SelectKnowledgeFiles(workDir, idx.MaxFiles, idx.MaxFileBytes)
	if err != nil {
		return err
	}
	prev, _ := idx.Store.Get(repo)
	if prev.Indexed == nil {
		prev.Indexed = map[string]FileState{}
	}
	next := RepoState{
		Repo:       repo,
		HeadSHA:    headSHA,
		Indexed:    map[string]FileState{},
		SummaryKey: prev.SummaryKey,
	}
	changed := 0
	for _, c := range cands {
		abs := filepath.Join(workDir, filepath.FromSlash(c.Rel))
		info, err := os.Stat(abs)
		if err != nil {
			continue
		}
		st := FileState{Size: info.Size(), ModTime: info.ModTime().UTC()}
		next.Indexed[c.Rel] = st
		if old, ok := prev.Indexed[c.Rel]; ok && old.equalish(st) {
			continue
		}
		raw, err := os.ReadFile(abs)
		if err != nil {
			return fmt.Errorf("读 %s: %w", c.Rel, err)
		}
		source := ragSource(repo, c.Rel)
		if _, err := idx.Agent.IndexText(ctx, source, c.Rel, string(raw)); err != nil {
			return fmt.Errorf("索引 %s: %w", c.Rel, err)
		}
		changed++
	}
	writeSummary := prev.SummaryKey == "" || changed > 0
	if err := idx.rememberSummary(repo, headSHA, cands, writeSummary); err != nil {
		log.Printf("项目记忆摘要写入失败 %s: %v", repo, err)
	} else if writeSummary {
		next.SummaryKey = summaryStateKey(repo)
	}
	return idx.Store.Put(repo, next)
}

func (idx *Indexer) rememberSummary(repo, headSHA string, cands []Candidate, write bool) error {
	if !write || idx.Agent == nil {
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "仓库 %s 的项目记忆索引。", repo)
	if headSHA != "" {
		fmt.Fprintf(&b, " 最近 head：%s。", shortSHA(headSHA))
	}
	b.WriteString(" 已收录文档：")
	if len(cands) == 0 {
		b.WriteString("（无）")
	} else {
		names := make([]string, 0, len(cands))
		for _, c := range cands {
			names = append(names, c.Rel)
		}
		b.WriteString(strings.Join(names, ", "))
	}
	b.WriteString("。审查时应优先遵守 AGENTS.md / README 中的边界与约定。")
	return idx.Agent.RememberLongTerm(b.String(), "project")
}

// ContextBundle 是注入审查提示词的项目记忆片段。
type ContextBundle struct {
	Memories []string
	RAGHits  []string
}

// LoadContext 在审查前提取与本次 PR 相关的记忆和 RAG 片段。失败返回空包。
// query 只用 repo + title + 短评论，不用完整 body，避免召回历史审查 prompt（issue #21）。
func (idx *Indexer) LoadContext(ctx context.Context, repo, title, body, comment string) ContextBundle {
	out := ContextBundle{}
	if idx == nil || idx.Agent == nil {
		return out
	}
	repo = strings.TrimSpace(repo)
	title = strings.TrimSpace(title)
	comment = strings.TrimSpace(comment)
	// body 保留参数兼容调用方，但不进 query，也不整段注入。
	_ = body
	query := strings.TrimSpace(strings.Join([]string{repo, title, clipQuery(comment, 200)}, " "))
	if query == "" {
		query = repo
	}
	for _, hit := range idx.Agent.Recall(query) {
		text := strings.TrimSpace(hit.Content)
		if text == "" || looksLikeReviewPrompt(text) {
			continue
		}
		out.Memories = append(out.Memories, text)
		if len(out.Memories) >= maxRenderMemories {
			break
		}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	hits, err := idx.Agent.SearchRAG(ctx, query, &sdk.RAGSearchOptions{TopK: maxRenderRAGHits, MinScore: 0.15})
	if err != nil {
		log.Printf("项目记忆 RAG 检索跳过 %s: %v", repo, err)
		return out
	}
	for _, hit := range hits {
		content := strings.TrimSpace(hit.Content)
		if content == "" || looksLikeReviewPrompt(content) {
			continue
		}
		label := hit.DocTitle
		if label == "" {
			label = hit.DocSource
		}
		if len(content) > maxMemoryChars {
			content = content[:maxMemoryChars] + "…"
		}
		if label != "" {
			out.RAGHits = append(out.RAGHits, fmt.Sprintf("[%s] %s", label, content))
		} else {
			out.RAGHits = append(out.RAGHits, content)
		}
		if len(out.RAGHits) >= maxRenderRAGHits {
			break
		}
	}
	return out
}

func clipQuery(s string, n int) string {
	s = strings.TrimSpace(s)
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n]
}

func ragSource(repo, rel string) string {
	return "repo:" + strings.ToLower(strings.TrimSpace(repo)) + ":" + filepath.ToSlash(rel)
}

func summaryStateKey(repo string) string {
	return "project-memory:" + strings.ToLower(strings.TrimSpace(repo))
}

func shortSHA(sha string) string {
	sha = strings.TrimSpace(sha)
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func (a FileState) equalish(b FileState) bool {
	if a.Size != b.Size {
		return false
	}
	d := a.ModTime.Sub(b.ModTime)
	if d < 0 {
		d = -d
	}
	return d <= time.Second
}
