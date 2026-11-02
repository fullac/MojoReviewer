// 验证发布校验拒绝虚构位置和标识符，并正确处理已读上下文与删除文件。
package review

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestGroundNotes(t *testing.T) {
	dir := symlinkReviewRepo(t)
	write(t, filepath.Join(dir, "a.go"), "package a\nfunc Existing() {}\nfunc Untouched() {}\n")
	write(t, filepath.Join(dir, "gone.go"), "package a\nfunc Deleted() {}\n")
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-m", "base")
	run(t, dir, "git", "update-ref", mergeBaseRef, "HEAD")
	write(t, filepath.Join(dir, "a.go"), "package a\nfunc Existing() { panic(1) }\nfunc Untouched() {}\n")
	os.Remove(filepath.Join(dir, "gone.go"))
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-m", "head")
	note := func(path string, line int, problem string) Note {
		return Note{Severity: "建议", Path: path, Line: line, Body: "- **问题描述：** " + problem + "\n\n- **代码位置：** `" + path + ":" + strconv.Itoa(line) + "`\n\n- **修复建议：** 新增 `NewHelper`。"}
	}
	for _, tc := range []struct {
		name  string
		n     Note
		reads []ReadRange
		want  int
	}{
		{"changed", note("a.go", 2, "`Existing` 会抛出异常。"), nil, 1},
		{"invented", note("a.go", 2, "`Missing` 不存在。"), nil, 0},
		{"unread context", note("a.go", 3, "上下文问题。"), nil, 0},
		{"read context", note("a.go", 3, "`Untouched` 上下文问题。"), []ReadRange{{Path: "a.go", Start: 3, End: 3}}, 1},
		{"deleted", note("gone.go", 2, "`Deleted` 被删除。"), nil, 1},
		{"outside", note("../a.go", 2, "问题。"), nil, 0},
		{"wrong line", note("a.go", 100, "问题。"), nil, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, rejected, err := Ground(context.Background(), dir, []Note{tc.n}, tc.reads)
			if err != nil || out.Len() != tc.want || rejected != 1-tc.want {
				t.Fatalf("got %v %d %v", out, rejected, err)
			}
		})
	}
	n := note("a.go", 2, "问题。")
	n.Body = strings.Replace(n.Body, "a.go:2", "a.go:3", 1)
	out, _, _ := Ground(context.Background(), dir, []Note{n}, nil)
	if out.Len() != 0 {
		t.Fatal("mismatched body accepted")
	}
	run(t, dir, "git", "update-ref", "-d", mergeBaseRef)
	out, _, err := Ground(context.Background(), dir, []Note{note("a.go", 2, "问题。")}, nil)
	if err == nil || out.Len() != 0 {
		t.Fatal("failed open without baseline")
	}
}

func TestReturnedRanges(t *testing.T) {
	got := ReturnedRanges("a.go", "a.go:1-400\n1\tfirst\n2\tpartial\n\n…输出过长，已截断。")
	if len(got) != 1 || got[0].Start != 1 || got[0].End != 1 {
		t.Fatalf("partial line counted: %+v", got)
	}
	if len(ReturnedRanges("a.go", "（a.go 共 1 行，起始行 400 超出范围）")) != 0 {
		t.Fatal("EOF receipt counted")
	}
	got = ReturnedRanges("a.go", "a.go:2-3\n2\ta\n3\tb\n")
	if len(got) != 1 || got[0].Start != 2 || got[0].End != 3 {
		t.Fatal(got)
	}
}

func TestGroundAndDiffTreatPathAsLiteral(t *testing.T) {
	dir := symlinkReviewRepo(t)
	for _, path := range []string{"a[1].go", "a1.go"} {
		write(t, filepath.Join(dir, path), "line1\nline2\n")
	}
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-m", "base")
	run(t, dir, "git", "update-ref", mergeBaseRef, "HEAD")
	write(t, filepath.Join(dir, "a[1].go"), "line1\nchanged literal\n")
	write(t, filepath.Join(dir, "a1.go"), "changed wildcard\nline2\n")
	run(t, dir, "git", "commit", "-am", "head")
	patch, err := (ToolScope{Dir: dir}).FileDiff("a[1].go")
	if err != nil || strings.Contains(patch, "changed wildcard") || !strings.Contains(patch, "changed literal") {
		t.Fatalf("%s %v", patch, err)
	}
	n := Note{Severity: "建议", Path: "a[1].go", Line: 1, Body: "- **问题描述：** 问题。\n\n- **代码位置：** `a[1].go:1`\n\n- **修复建议：** 修复。"}
	out, _, err := Ground(context.Background(), dir, []Note{n}, nil)
	if err != nil || out.Len() != 0 {
		t.Fatal("another file supplied changed-line evidence", err)
	}
}
