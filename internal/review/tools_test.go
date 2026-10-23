package review

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCleanRejectsEscape(t *testing.T) {
	dir := t.TempDir()
	scope := ToolScope{Dir: dir}
	if _, err := scope.clean("../secret"); err == nil {
		t.Fatal(".. 应被拒绝")
	}
	outside := filepath.Join(filepath.Dir(dir), "outside.txt")
	if _, err := scope.clean(outside); err == nil {
		t.Fatal("检出外绝对路径应被拒绝")
	}
	if _, err := os.Create(filepath.Join(dir, "keep.go")); err != nil {
		t.Fatal(err)
	}
	rel, err := scope.clean(filepath.Join(dir, "keep.go"))
	if err != nil {
		t.Fatal(err)
	}
	if rel != "keep.go" {
		t.Fatalf("相对路径 = %s", rel)
	}
}

func TestReadFileRange(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := (ToolScope{Dir: dir}).ReadFile("a.txt", 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "2\ttwo") || !strings.Contains(got, "3\tthree") {
		t.Fatalf("读取结果异常:\n%s", got)
	}
}

func TestReviewToolsOnCheckout(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, "git", "init", "-b", "main")
	run(t, dir, "git", "config", "user.email", "test@example.com")
	run(t, dir, "git", "config", "user.name", "test")
	write(t, filepath.Join(dir, "calc.go"), "package calc\n\nfunc Add(a, b int) int { return a + b }\n")
	write(t, filepath.Join(dir, "calc_test.go"), "package calc\n\nfunc TestAdd(t *testing.T) {}\n")
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-m", "base")
	base := strings.TrimSpace(run(t, dir, "git", "rev-parse", "HEAD"))

	write(t, filepath.Join(dir, "calc.go"), "package calc\n\nfunc Add(a, b int) int { return a + b }\n\nfunc Sub(a, b int) int { return a - b }\n")
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-m", "head")
	run(t, dir, "git", "update-ref", baseRef, base)
	run(t, dir, "git", "update-ref", mergeBaseRef, base)

	scope := ToolScope{Dir: dir}
	stat, err := scope.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stat, "calc.go") {
		t.Fatalf("stat 缺少文件:\n%s", stat)
	}
	diff, err := scope.FileDiff("calc.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "func Sub") {
		t.Fatalf("file diff 缺少改动:\n%s", diff)
	}
	if _, err := scope.FileDiff("../outside"); err == nil {
		t.Fatal("file diff 应拒绝越界路径")
	}
	symbols, err := scope.ChangedSymbols()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(symbols, "Sub") {
		t.Fatalf("符号缺少 Sub:\n%s", symbols)
	}
	tests, err := scope.TestsAround("calc.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tests, "calc_test.go") {
		t.Fatalf("附近测试缺少 calc_test.go:\n%s", tests)
	}
	grep, err := scope.Grep("func Sub", "*.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(grep, "calc.go") {
		t.Fatalf("grep 未命中:\n%s", grep)
	}
}

func TestNotesRender(t *testing.T) {
	var notes Notes
	if err := notes.Add("严重", "calc.go", 4, "减法没有处理下溢"); err != nil {
		t.Fatal(err)
	}
	if err := notes.Add("无所谓", "a.go", 1, "不该接受"); err == nil {
		t.Fatal("非法 severity 应被拒绝")
	}
	got := notes.Render()
	if !strings.HasPrefix(got, BotPrefix) || !strings.Contains(got, "calc.go:4") || strings.Contains(got, "🟡 建议") {
		t.Fatalf("评论格式异常:\n%s", got)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestNotesRenderMarkdown(t *testing.T) {
	var notes Notes
	body := "- **问题：** 满足重试条件时仍接受任务。\n\n- **影响：** 重启后可能丢失任务。\n\n- **建议修复：** 返回保存错误。\n\n  *建议修改示例：*\n\n  ```go\n  if err != nil {\n  \treturn err\n  }\n  ```"
	if err := notes.AddWithTitle("建议", "test.go", 0, "补充重复投递测试", "- **建议调整：** 覆盖重复请求。"); err != nil {
		t.Fatal(err)
	}
	if err := notes.AddWithTitle("严重", "server.go", 123, "保存失败仍接受任务", body); err != nil {
		t.Fatal(err)
	}
	got := notes.Render()
	for _, want := range []string{
		BotPrefix + "\n\n本轮记录 **1 个严重问题、1 条建议**。",
		"### 🔴 严重问题\n\n**1. 保存失败仍接受任务**",
		"📍 ` server.go:123 `\n\n" + body,
		"### 🟡 建议\n\n**2. 补充重复投递测试**",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in comment:\n%s", want, got)
		}
	}
	if strings.Index(got, "### 🔴") > strings.Index(got, "### 🟡") {
		t.Fatal("严重问题应优先展示")
	}
}

func TestNotesRenderEmptyAndOptionalFields(t *testing.T) {
	var empty *Notes
	if got := empty.Render(); got != BotPrefix+"\n\n本轮未记录审查意见；这不代表所有改动均已核实。" {
		t.Fatalf("empty result must not claim a complete review: %s", got)
	}
	var notes Notes
	if err := notes.Add("建议", "", 0, "保留旧调用的正文"); err != nil {
		t.Fatal(err)
	}
	got := notes.Render()
	if strings.Contains(got, "🔴") || strings.Contains(got, "📍") || !strings.Contains(got, "**1. 审查意见**\n\n保留旧调用的正文") {
		t.Fatalf("optional fields: %s", got)
	}
	if err := notes.AddWithTitle("建议", "", 0, "标题\n换行", "内容"); err == nil || notes.Len() != 1 {
		t.Fatal("multiline title must be rejected without recording a note")
	}
}
