package projectmem

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/yurika0211/luckyagent-app/sdk"
)

// Indexer 把仓库高信号文档写入 LuckyAgent 隔离家目录里的 RAG / 记忆。
type Indexer struct {
	Agent        Backend
	Store        *Store
	MaxFiles     int
	MaxFileBytes int
}

// Backend 是项目记忆使用的公开 SDK 接口，便于离线验证缓存和失败路径。
type Backend interface {
	IndexText(context.Context, string, string, string) (*sdk.DocumentInfo, error)
	RememberLongTerm(string, string) error
	Recall(string) []sdk.MemoryHit
	SearchRAG(context.Context, string, *sdk.RAGSearchOptions) ([]sdk.RAGHit, error)
}

type IndexResult struct {
	CacheHit bool
	Files    int
}

// Ensure 只在索引和元数据都成功后缓存 head；错误交给宿主归类记录，不阻断审查。
func (idx *Indexer) Ensure(ctx context.Context, repo, workDir, headSHA string) (IndexResult, error) {
	if idx == nil || idx.Agent == nil || idx.Store == nil {
		return IndexResult{}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	repo = strings.ToLower(strings.TrimSpace(repo))
	if repo == "" || strings.TrimSpace(workDir) == "" {
		return IndexResult{}, fmt.Errorf("项目记忆输入为空")
	}
	if err := ctx.Err(); err != nil {
		return IndexResult{}, err
	}
	prev, _ := idx.Store.Get(repo)
	if headSHA != "" && prev.HeadSHA == headSHA && prev.Version == 1 {
		return IndexResult{CacheHit: true, Files: len(prev.Indexed)}, nil
	}
	cands, err := SelectKnowledgeFiles(workDir, idx.MaxFiles, idx.MaxFileBytes)
	if err != nil {
		return IndexResult{}, err
	}
	next := RepoState{Repo: repo, HeadSHA: headSHA, Version: 1, Indexed: map[string]FileState{}, SummaryKey: prev.SummaryKey}
	changed := 0
	for _, c := range cands {
		if err := ctx.Err(); err != nil {
			return IndexResult{}, err
		}
		abs, info, err := resolveKnowledgeFile(workDir, c.Rel)
		if err != nil {
			return IndexResult{}, fmt.Errorf("知识文件路径变化")
		}
		raw, err := os.ReadFile(abs)
		if err != nil {
			return IndexResult{}, err
		}
		st := FileState{Size: info.Size(), ModTime: info.ModTime().UTC(), Hash: fmt.Sprintf("%x", sha256.Sum256(raw))}
		next.Indexed[c.Rel] = st
		if old, ok := prev.Indexed[c.Rel]; ok && old.Hash == st.Hash {
			continue
		}
		if _, err := idx.Agent.IndexText(ctx, ragSource(repo, c.Rel), c.Rel, string(raw)); err != nil {
			return IndexResult{}, err
		}
		changed++
	}
	if err := ctx.Err(); err != nil {
		return IndexResult{}, err
	}
	writeSummary := prev.SummaryKey == "" || changed > 0 || len(prev.Indexed) != len(next.Indexed)
	if err := idx.rememberSummary(repo, headSHA, cands, writeSummary); err != nil {
		return IndexResult{}, err
	}
	if writeSummary {
		next.SummaryKey = summaryStateKey(repo)
	}
	if err := ctx.Err(); err != nil {
		return IndexResult{}, err
	}
	if err := idx.Store.Put(repo, next); err != nil {
		return IndexResult{}, err
	}
	return IndexResult{Files: len(next.Indexed)}, nil
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
// 查询包含仓库、短标题/评论、改动路径和符号；不用完整 body 或补丁。
func (idx *Indexer) LoadContext(ctx context.Context, repo, title, body, comment string, changes ...string) ContextBundle {
	out := ContextBundle{}
	if idx == nil || idx.Agent == nil {
		return out
	}
	repo = strings.ToLower(strings.TrimSpace(repo))
	title = strings.TrimSpace(title)
	comment = strings.TrimSpace(comment)
	// body 保留参数兼容调用方，但不进 query，也不整段注入。
	_ = body
	query := buildQuery(repo, title, comment, changes)
	if query == "" {
		query = repo
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return out
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

	if ctx.Err() != nil {
		return out
	}
	hits, err := idx.Agent.SearchRAG(ctx, query, &sdk.RAGSearchOptions{TopK: maxRenderRAGHits * 4, MinScore: 0.15})
	if err != nil {
		log.Printf("项目记忆检索失败，继续审查 repo=%s", repo)
		return out
	}
	for _, hit := range hits {
		if !strings.HasPrefix(hit.DocSource, ragSource(repo, "")) {
			continue
		}
		content := strings.TrimSpace(hit.Content)
		if content == "" || looksLikeReviewPrompt(content) {
			continue
		}
		label := hit.DocTitle
		if label == "" {
			label = hit.DocSource
		}
		if len([]rune(content)) > maxMemoryChars {
			content = clipQuery(content, maxMemoryChars) + "…"
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
	runes := []rune(s)
	if n <= 0 || len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

func buildQuery(repo, title, comment string, changes []string) string {
	parts := []string{clipQuery(repo, 200), clipQuery(title, 200), clipQuery(comment, 200)}
	seen := map[string]bool{}
	for _, term := range changes {
		if len(parts) >= 35 {
			break
		}
		term = clipQuery(term, 120)
		if term != "" && !seen[term] {
			parts = append(parts, term)
			seen[term] = true
		}
	}
	return clipQuery(strings.Join(parts, " "), 1200)
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
