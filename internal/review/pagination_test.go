package review

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestStatPagesCoverLargeFileMap(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, "git", "init", "-b", "main")
	run(t, dir, "git", "config", "user.name", "test")
	run(t, dir, "git", "config", "user.email", "test@example.com")
	run(t, dir, "git", "commit", "--allow-empty", "-m", "base")
	run(t, dir, "git", "update-ref", baseRef, "HEAD")
	prefix := strings.Repeat("directory", 22)
	if err := os.Mkdir(filepath.Join(dir, prefix), 0o755); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	for i := 0; i < 120; i++ {
		name := filepath.Join(prefix, strings.Repeat("file", 48)+fmt.Sprintf("-%03d.go", i))
		write(t, filepath.Join(dir, name), "package fixture\n")
		want[filepath.ToSlash(name)] = true
	}
	for _, name := range []string{"file with spaces.go", " leading space.go", "中文.go"} {
		write(t, filepath.Join(dir, name), "package fixture\n")
		want[name] = true
	}
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-m", "many files")
	scope := ToolScope{Dir: dir}
	for _, limit := range []int{17, 200} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			seen := map[string]bool{}
			offset := 0
			for page := 0; page < 20; page++ {
				out, err := scope.StatPage(offset, limit)
				if err != nil {
					t.Fatal(err)
				}
				if len(out) > maxStatBytes {
					t.Fatalf("输出超过字节上限: %d", len(out))
				}
				if !strings.Contains(out, "patch_truncated: true") {
					t.Fatal("大 patch 标记缺失")
				}
				next := -1
				for _, line := range strings.Split(out, "\n") {
					if value, ok := strings.CutPrefix(line, "next_offset: "); ok {
						next, err = strconv.Atoi(value)
						if err != nil {
							t.Fatal(err)
						}
					}
					if path, _, ok := strings.Cut(line, "\t+"); ok {
						if seen[path] || !want[path] {
							t.Fatalf("重复或损坏的文件记录: %q", path)
						}
						seen[path] = true
					}
				}
				if next == -1 {
					if !strings.Contains(out, "has_more: false") || len(seen) != len(want) {
						t.Fatalf("文件列表没有完整返回: got %d, want %d", len(seen), len(want))
					}
					return
				}
				if next <= offset || !strings.Contains(out, "has_more: true") {
					t.Fatalf("不可续取的页: %s", out)
				}
				offset = next
			}
			t.Fatal("分页未收束")
		})
	}
	if _, err := scope.StatPage(-1, 50); err == nil {
		t.Fatal("负 offset 应拒绝")
	}
	if _, err := scope.StatPage(0, -1); err == nil {
		t.Fatal("负 limit 应拒绝")
	}
	out, err := scope.StatPage(len(want)+10, 50)
	if err != nil || !strings.Contains(out, "next_offset: -1") || !strings.Contains(out, "has_more: false") {
		t.Fatalf("越过末页应结束: %s %v", out, err)
	}
}
