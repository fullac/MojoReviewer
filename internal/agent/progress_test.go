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

// 回执汇总共享，但内容去重只能留在一个分工内部：另一路必须真的拿到那份
// patch，否则它只能反复重试到超时。
func TestSharedReceiptStillDeliversContentToOtherRoutes(t *testing.T) {
	receipt := &readProgress{}
	mapRoute := &readProgress{sink: receipt}
	behaviorRoute := &readProgress{sink: receipt}
	key := readKey{kind: "pr_file_diff", path: "internal/server/server.go"}

	first, err := mapRoute.read(key, func() (string, error) { return "patch from map", nil })
	if err != nil || first != "patch from map" {
		t.Fatalf("first read=%q err=%v", first, err)
	}
	second, err := behaviorRoute.read(key, func() (string, error) { return "patch from behavior", nil })
	if err != nil {
		t.Fatal(err)
	}
	if second != "patch from behavior" {
		t.Fatalf("另一路被错误地告知已读: %q", second)
	}
	// 同一分工内重复读才该被拦。
	again, err := mapRoute.read(key, func() (string, error) { return "never", nil })
	if err != nil || !strings.Contains(again, "本轮已读") {
		t.Fatalf("同一分工内应去重: %q err=%v", again, err)
	}
	summary := receipt.summary()
	if !strings.Contains(summary, "patch 1 个文件") || !strings.Contains(summary, "server.go") {
		t.Fatalf("回执没有汇总两路的读取: %s", summary)
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
