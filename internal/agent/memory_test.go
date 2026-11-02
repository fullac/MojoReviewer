// 验证记忆准备使用独立预算，后续模型仍获得完整超时窗口，并携带改动查询。
package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mojoreviewer/mojoreviewer/internal/config"
	"github.com/mojoreviewer/mojoreviewer/internal/github"
	"github.com/mojoreviewer/mojoreviewer/internal/observe"
	"github.com/mojoreviewer/mojoreviewer/internal/projectmem"
	"github.com/mojoreviewer/mojoreviewer/internal/review"
	"github.com/yurika0211/luckyagent-app/sdk"
)

type slowMemory struct {
	query  string
	budget time.Duration
}

func (m *slowMemory) IndexText(ctx context.Context, _, _, _ string) (*sdk.DocumentInfo, error) {
	deadline, _ := ctx.Deadline()
	m.budget = time.Until(deadline)
	select {
	case <-time.After(1100 * time.Millisecond):
		return &sdk.DocumentInfo{}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (*slowMemory) RememberLongTerm(string, string) error { return nil }
func (m *slowMemory) Recall(q string) []sdk.MemoryHit     { m.query = q; return nil }
func (m *slowMemory) SearchRAG(_ context.Context, q string, _ *sdk.RAGSearchOptions) ([]sdk.RAGHit, error) {
	m.query = q
	return nil, nil
}

func TestMemoryPreparationDoesNotConsumeModelDeadline(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The runtime is configured non-streaming; helper also supports streamed replies in other tests.
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"one","choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`))
	}))
	defer model.Close()
	root := t.TempDir()
	cfg := config.AgentConfig{HomeDir: filepath.Join(root, "runtime"), Provider: "openai-compatible", Model: "test", APIKey: "test", APIBase: model.URL + "/v1", TaskTimeout: time.Second}
	runner, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()
	st, _ := projectmem.Open(filepath.Join(root, "memory.json"))
	memory := &slowMemory{}
	runner.memory = &projectmem.Indexer{Store: st, Agent: memory}
	work := t.TempDir()
	os.WriteFile(filepath.Join(work, "README.md"), []byte("documentation"), 0600)
	for _, args := range [][]string{{"init"}, {"config", "user.name", "test"}, {"config", "user.email", "test@example.test"}, {"add", "."}, {"commit", "-m", "fixture"}, {"update-ref", "refs/review/merge-base", "HEAD"}} {
		if out, err := exec.Command("git", append([]string{"-C", work}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	input := ReviewInput{WorkDir: work, Review: github.Review{Repo: "acme/web", HeadSHA: "A", SessionKey: "test"}, Manifest: &review.Manifest{Files: []review.ChangedFile{{Path: "auth.go", Symbols: []review.ChangedSymbol{{Name: "Check"}}}}}}
	ctx, metrics := observe.Start(context.Background())
	input.Memory = runner.PrepareMemory(ctx, input)
	if memory.budget < 20*time.Second || !strings.Contains(memory.query, "auth.go") || !strings.Contains(memory.query, "Check") {
		t.Fatal("preparation used model budget or lost change hints")
	}
	if _, err := runner.Review(ctx, input); err != nil {
		t.Fatalf("memory consumed model timeout: %v", err)
	}
	snapshot := metrics.Snapshot()
	if snapshot.Stages["memory"] < 1000 || snapshot.MemoryCacheHit == nil || *snapshot.MemoryCacheHit || snapshot.MemoryFiles != 1 {
		t.Fatalf("missing memory metrics: %+v", snapshot)
	}
	runner.PrepareMemory(ctx, input)
	if snapshot := metrics.Snapshot(); snapshot.MemoryCacheHit == nil || !*snapshot.MemoryCacheHit {
		t.Fatal("same head cache not observed")
	}
}
