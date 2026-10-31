// 验证按提交缓存、失败重试、内容变化识别，以及改动查询的字符和仓库边界。
package projectmem

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/yurika0211/luckyagent-app/sdk"
)

type memoryBackend struct {
	indexes int
	fail    bool
	query   string
	hits    []sdk.RAGHit
}

func (b *memoryBackend) IndexText(ctx context.Context, source, title, content string) (*sdk.DocumentInfo, error) {
	b.indexes++
	if b.fail {
		return nil, errors.New("private failure")
	}
	return &sdk.DocumentInfo{}, ctx.Err()
}
func (b *memoryBackend) RememberLongTerm(string, string) error { return nil }
func (b *memoryBackend) Recall(q string) []sdk.MemoryHit       { b.query = q; return nil }
func (b *memoryBackend) SearchRAG(ctx context.Context, q string, _ *sdk.RAGSearchOptions) ([]sdk.RAGHit, error) {
	b.query = q
	return b.hits, ctx.Err()
}

func TestIndexCachesOnlySuccessfulHeadsAndHashesContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "README.md")
	os.WriteFile(path, []byte("first"), 0600)
	statePath := filepath.Join(t.TempDir(), "memory.json")
	st, _ := Open(statePath)
	b := &memoryBackend{}
	idx := &Indexer{Agent: b, Store: st}
	first, err := idx.Ensure(context.Background(), "Acme/Web", dir, "A")
	if err != nil || first.CacheHit || first.Files != 1 || b.indexes != 1 {
		t.Fatalf("first index: %+v %v", first, err)
	}
	reopened, _ := Open(statePath)
	idx.Store = reopened
	cached, err := idx.Ensure(context.Background(), "acme/web", dir, "A")
	if err != nil || !cached.CacheHit || b.indexes != 1 {
		t.Fatal("persisted head cache missed")
	}
	before, _ := os.Stat(path)
	os.WriteFile(path, []byte("other"), 0600)
	os.Chtimes(path, before.ModTime(), before.ModTime())
	b.fail = true
	if _, err := idx.Ensure(context.Background(), "acme/web", dir, "B"); err == nil {
		t.Fatal("index error lost")
	}
	saved, _ := idx.Store.Get("acme/web")
	if saved.HeadSHA != "A" {
		t.Fatal("failed head cached")
	}
	b.fail = false
	next, err := idx.Ensure(context.Background(), "acme/web", dir, "B")
	if err != nil || next.CacheHit || b.indexes != 3 {
		t.Fatalf("same-size/same-time edit not reindexed: %+v %v %d", next, err, b.indexes)
	}
	saved, _ = idx.Store.Get("acme/web")
	if saved.Indexed["README.md"].Hash == "" {
		t.Fatal("content hash absent")
	}
}

func TestLegacyCacheAndFailedPersistence(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("docs"), 0600)
	path := filepath.Join(t.TempDir(), "memory.json")
	st, _ := Open(path)
	st.Put("repo", RepoState{HeadSHA: "A", Indexed: map[string]FileState{}})
	b := &memoryBackend{}
	idx := &Indexer{Agent: b, Store: st}
	result, err := idx.Ensure(context.Background(), "repo", dir, "A")
	if err != nil || result.CacheHit || b.indexes != 1 {
		t.Fatal("legacy metadata incorrectly reused")
	}
	os.Remove(path)
	os.Mkdir(path, 0700)
	if _, err := idx.Ensure(context.Background(), "repo", dir, "B"); err == nil {
		t.Fatal("metadata write error lost")
	}
	saved, _ := st.Get("repo")
	if saved.HeadSHA != "A" {
		t.Fatal("failed persistence updated cache")
	}
	os.Remove(path)
	if _, err := idx.Ensure(context.Background(), "repo", dir, "B"); err != nil {
		t.Fatal(err)
	}
}

func TestContextQueryUsesChangesAndFiltersOtherRepositories(t *testing.T) {
	b := &memoryBackend{hits: []sdk.RAGHit{{DocSource: "repo:acme/web-evil:README.md", Content: "foreign"}, {DocSource: "repo:acme/web:README.md", Content: strings.Repeat("中文", 300)}}}
	idx := &Indexer{Agent: b}
	result := idx.LoadContext(context.Background(), "acme/web", "修复权限", "BODY_MUST_NOT_ENTER_QUERY", "检查", "internal/auth.go", "CheckPermission")
	for _, want := range []string{"acme/web", "修复权限", "internal/auth.go", "CheckPermission"} {
		if !strings.Contains(b.query, want) {
			t.Fatalf("missing query term: %s", b.query)
		}
	}
	if strings.Contains(b.query, "BODY_MUST") || len(result.RAGHits) != 1 || strings.Contains(result.Render(), "foreign") || !utf8.ValidString(result.Render()) {
		t.Fatal("context boundary failure")
	}
	query := buildQuery("repo", strings.Repeat("中", 500), strings.Repeat("文", 500), []string{strings.Repeat("字", 2000)})
	if len([]rune(query)) > 1200 || !utf8.ValidString(query) {
		t.Fatal("query is not bounded UTF-8")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	b.query = ""
	if bundle := idx.LoadContext(ctx, "repo", "title", "", "comment"); len(bundle.RAGHits) > 0 || b.query != "" {
		t.Fatal("cancelled preparation continued retrieval")
	}
}
