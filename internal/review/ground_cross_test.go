// 验证跨文件依据只使用已读行段，并保持路径、旧侧和体积边界。
package review

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGroundAcceptsIdentifierInAnotherReadRange(t *testing.T) {
	dir := symlinkReviewRepo(t)
	write(t, filepath.Join(dir, "lookup.go"), "package p\nfunc Lookup() *int { v := 1; return &v }\n")
	write(t, filepath.Join(dir, "handler.go"), "package p\nfunc Handle() int { return *Lookup() }\n")
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-m", "base")
	run(t, dir, "git", "update-ref", mergeBaseRef, "HEAD")
	write(t, filepath.Join(dir, "lookup.go"), "package p\nfunc Lookup() *int { return nil }\n")
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-m", "head")

	note := func(problem string) Note {
		return Note{Severity: "建议", Path: "lookup.go", Line: 2, Body: "- **问题描述：** " + problem + "\n\n- **代码位置：** `lookup.go:2`\n\n- **修复建议：** 让 `Handle` 检查空值。"}
	}
	both := []ReadRange{{Path: "handler.go", Start: 2, End: 2}}
	out, rejected, err := Ground(context.Background(), dir, []Note{note("`Lookup` 现在返回 nil，但 `Handle` 仍直接解引用。")}, both)
	if err != nil || out.Len() != 1 || rejected != 0 {
		t.Fatalf("cross-file read rejected: %v %d %v", out, rejected, err)
	}

	unread := []ReadRange{{Path: "handler.go", Start: 1, End: 1}}
	out, rejected, err = Ground(context.Background(), dir, []Note{note("`Lookup` 现在返回 nil，但 `Handle` 仍直接解引用。")}, unread)
	if err != nil || out.Len() != 0 || rejected != 1 {
		t.Fatalf("unread Handle accepted: %v %d %v", out, rejected, err)
	}

	missing := []ReadRange{{Path: "handler.go", Start: 2, End: 2}}
	out, rejected, err = Ground(context.Background(), dir, []Note{note("`Lookup` 调用了 `Missing`。")}, missing)
	if err != nil || out.Len() != 0 || rejected != 1 {
		t.Fatalf("invented identifier accepted: %v %d %v", out, rejected, err)
	}

	outsideLine := []ReadRange{{Path: "handler.go", Start: 2, End: 2}, {Path: "lookup.go", Start: 1, End: 1}}
	out, _, err = Ground(context.Background(), dir, []Note{note("`package` 声明与 `Handle` 不一致。")}, outsideLine)
	if err != nil || out.Len() != 1 {
		t.Fatal("identifier on the located file was confined to the read range", err)
	}
	out, _, err = Ground(context.Background(), dir, []Note{note("`found` 已不存在，`Handle` 仍解引用。")}, outsideLine)
	if err != nil || out.Len() != 0 {
		t.Fatal("identifier outside every read range was accepted", err)
	}
}

func TestGroundDeletedReadRangeUsesOldSide(t *testing.T) {
	dir := symlinkReviewRepo(t)
	write(t, filepath.Join(dir, "old.go"), "package p\nfunc Removed() {}\n")
	write(t, filepath.Join(dir, "live.go"), "package p\nfunc Live() {}\n")
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-m", "base")
	run(t, dir, "git", "update-ref", mergeBaseRef, "HEAD")
	os.Remove(filepath.Join(dir, "old.go"))
	write(t, filepath.Join(dir, "live.go"), "package p\nfunc Live() { sink() }\n")
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-m", "head")
	note := Note{Severity: "建议", Path: "live.go", Line: 2, Body: "- **问题描述：** `Live` 丢掉了 `Removed` 的调用。\n\n- **代码位置：** `live.go:2`\n\n- **修复建议：** 恢复调用。"}
	out, rejected, err := Ground(context.Background(), dir, []Note{note}, []ReadRange{{Path: "old.go", Start: 2, End: 2, Old: true}})
	if err != nil || out.Len() != 1 || rejected != 0 {
		t.Fatalf("old side rejected: %v %d %v", out, rejected, err)
	}
	out, rejected, err = Ground(context.Background(), dir, []Note{note}, []ReadRange{{Path: "old.go", Start: 2, End: 2}})
	if err != nil || out.Len() != 0 || rejected != 1 {
		t.Fatalf("new side of deleted file accepted: %v %d %v", out, rejected, err)
	}
}

func TestGroundReadEvidenceStaysInsideCheckoutAndBudget(t *testing.T) {
	dir := symlinkReviewRepo(t)
	outside := t.TempDir()
	write(t, filepath.Join(outside, "secret.go"), "package p\nfunc Secret() {}\n")
	write(t, filepath.Join(dir, "live.go"), "package p\nfunc Live() {}\n")
	write(t, filepath.Join(dir, "handler.go"), "package p\nfunc Handle() int { return *Lookup() }\n")
	write(t, filepath.Join(dir, "wide.go"), "package p\nfunc Pad() {}\nfunc Gap() {}\n"+strings.Repeat("func Pad() {}\n", 39))
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-m", "base")
	run(t, dir, "git", "update-ref", mergeBaseRef, "HEAD")
	symlink(t, filepath.Join(outside, "secret.go"), filepath.Join(dir, "secret.go"))
	write(t, filepath.Join(dir, "live.go"), "package p\nfunc Live() { sink() }\n")
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-m", "head")
	note := func(problem string) Note {
		return Note{Severity: "建议", Path: "live.go", Line: 2, Body: "- **问题描述：** " + problem + "\n\n- **代码位置：** `live.go:2`\n\n- **修复建议：** 修复。"}
	}
	out, _, err := Ground(context.Background(), dir, []Note{note("`Live` 暴露了 `Secret`。")}, []ReadRange{{Path: "secret.go", Start: 2, End: 2}, {Path: "../secret.go", Start: 2, End: 2}})
	if err != nil || out.Len() != 0 {
		t.Fatal("identifier reached through an escaping path", err)
	}

	reads := make([]ReadRange, 0, 42)
	for i := 2; i <= 41; i++ {
		reads = append(reads, ReadRange{Path: "wide.go", Start: i, End: i})
	}
	reads = append(reads, ReadRange{Path: "handler.go", Start: 2, End: 2})
	out, _, err = Ground(context.Background(), dir, []Note{note("`Live` 仍引用 `Handle`。")}, reads)
	if err != nil || out.Len() != 1 {
		t.Fatal("per-line patch receipts exhausted the later file", err)
	}
	gapped := []ReadRange{{Path: "wide.go", Start: 2, End: 2}, {Path: "wide.go", Start: 4, End: 4}}
	out, _, err = Ground(context.Background(), dir, []Note{note("`Live` 与未读的 `Gap` 重复。")}, gapped)
	if err != nil || out.Len() != 0 {
		t.Fatal("gap between read ranges on one file side was included", err)
	}
	many := make([]ReadRange, 0, 34)
	for i := 0; i < 33; i++ {
		name := fmt.Sprintf("side%02d.go", i)
		write(t, filepath.Join(dir, name), "package p\nfunc Side() {}\n")
		many = append(many, ReadRange{Path: name, Start: 2, End: 2})
	}
	many = append(many, ReadRange{Path: "handler.go", Start: 2, End: 2})
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-m", "sides")
	out, _, err = Ground(context.Background(), dir, []Note{note("`Live` 仍引用 `Handle`。")}, many)
	if err != nil || out.Len() != 0 {
		t.Fatal("file side past the evidence budget was loaded", err)
	}

	huge := "package p\n" + strings.Repeat("x", groundReadByteLimit) + "\nfunc Huge() {}\n"
	write(t, filepath.Join(dir, "huge.go"), huge)
	run(t, dir, "git", "add", "huge.go")
	run(t, dir, "git", "commit", "-m", "huge")
	out, _, err = Ground(context.Background(), dir, []Note{note("`Live` 调用了 `Huge`。")}, []ReadRange{{Path: "huge.go", Start: 1, End: 3}})
	if err != nil || out.Len() != 0 {
		t.Fatal("oversized read span entered the corpus", err)
	}
}

func TestGroundLargeFileKeepsSmallReadEvidence(t *testing.T) {
	dir := symlinkReviewRepo(t)
	padding := strings.Repeat("// padding\n", groundReadByteLimit/10+1)
	write(t, filepath.Join(dir, "lookup.go"), "package p\nfunc Lookup() *int { v := 1; return &v }\n"+padding)
	write(t, filepath.Join(dir, "handler.go"), "package p\nfunc Handle() int { return *Lookup() }\n"+padding)
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-m", "base")
	run(t, dir, "git", "update-ref", mergeBaseRef, "HEAD")
	write(t, filepath.Join(dir, "lookup.go"), "package p\nfunc Lookup() *int { return nil }\n"+padding)
	run(t, dir, "git", "commit", "-am", "head")
	n := Note{Severity: "严重", Path: "lookup.go", Line: 2, Body: "- **问题描述：** `Lookup` 返回空指针，`Handle` 解引用会崩溃。\n\n- **代码位置：** `lookup.go:2`\n\n- **修复建议：** 检查空值。"}
	out, rejected, err := Ground(context.Background(), dir, []Note{n}, []ReadRange{{Path: "lookup.go", Start: 2, End: 2}, {Path: "handler.go", Start: 2, End: 2}})
	if err != nil || rejected != 0 || out.Len() != 1 {
		t.Fatalf("unread file bytes exhausted evidence budget: kept=%d rejected=%d err=%v", out.Len(), rejected, err)
	}
}
