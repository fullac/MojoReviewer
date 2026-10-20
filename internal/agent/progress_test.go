package agent

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/mojoreviewer/mojoreviewer/internal/review"
)

func TestReadProgressDeduplicatesConcurrentRequests(t *testing.T) {
	reads := &readProgress{}
	var calls atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := reads.read(readKey{kind: "pr_file_diff", path: "a.go"}, func() (string, error) {
				calls.Add(1)
				return "patch", nil
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 || !strings.Contains(reads.summary(), "重复读取 11 次") {
		t.Fatalf("重复执行了读取: %d %s", calls.Load(), reads.summary())
	}
}

func TestReadProgressDoesNotCacheFailure(t *testing.T) {
	reads := &readProgress{}
	key := readKey{kind: "pr_file_diff", path: "a.go"}
	if _, err := reads.read(key, func() (string, error) { return "", errors.New("temporary error") }); err == nil {
		t.Fatal("错误不应丢失")
	}
	text, err := reads.read(key, func() (string, error) { return "retried patch", nil })
	if err != nil || text != "retried patch" {
		t.Fatalf("失败的读取应可重试: %s %v", text, err)
	}
}

func TestReviewReadsDeduplicatePathsAndRanges(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-b", "main")
	git("config", "user.name", "test")
	git("config", "user.email", "test@example.com")
	path := filepath.Join(dir, "a.go")
	if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "base")
	git("update-ref", "refs/review/base", "HEAD")
	git("update-ref", "refs/review/merge-base", "HEAD")
	if err := os.WriteFile(path, []byte("after\nsecond\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("commit", "-am", "head")
	tools := reviewTools{scope: review.ToolScope{Dir: dir}, notes: &review.Notes{}, reads: &readProgress{}}
	patch, err := tools.fileDiff(map[string]any{"path": "./a.go"})
	if err != nil || !strings.Contains(patch, "+after") {
		t.Fatalf("首次 patch 读取异常: %s %v", patch, err)
	}
	for i := 0; i < 2; i++ {
		duplicate, err := tools.fileDiff(map[string]any{"path": path})
		if err != nil || !strings.Contains(duplicate, "本轮已读") || strings.Contains(duplicate, "+after") {
			t.Fatalf("重复 patch 应给去重提示: %s %v", duplicate, err)
		}
	}
	if _, err := tools.readFile(map[string]any{"path": "a.go"}); err != nil {
		t.Fatal(err)
	}
	duplicate, err := tools.readFile(map[string]any{"path": path, "start_line": 1, "end_line": 400})
	if err != nil || !strings.Contains(duplicate, "本轮已读") {
		t.Fatalf("等价的行段请求应去重: %s %v", duplicate, err)
	}
	next, err := tools.readFile(map[string]any{"path": "a.go", "start_line": 2, "end_line": 2})
	if err != nil || !strings.Contains(next, "2\tsecond") {
		t.Fatalf("新的行段必须可读: %s %v", next, err)
	}
	// 新一轮/新提交不复用旧回执；fix 可写工具不启用去重。
	tools.reads = &readProgress{}
	if out, err := tools.fileDiff(map[string]any{"path": "a.go"}); err != nil || !strings.Contains(out, "+after") {
		t.Fatalf("新一轮仍被旧回执拦截: %s %v", out, err)
	}
	tools.reads = nil
	for _, content := range []string{"edit one", "edit two"} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		out, err := tools.readFile(map[string]any{"path": "a.go"})
		if err != nil || !strings.Contains(out, content) {
			t.Fatalf("fix 路径应始终读取新内容: %s %v", out, err)
		}
	}
}
