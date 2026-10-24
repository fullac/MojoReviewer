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
	write(t, filepath.Join(dir, "go.mod"), "module example.com/calc\n\ngo 1.25.0\n")
	write(t, filepath.Join(dir, "calc.go"), "package calc\n\nfunc Add(a, b int) int { return a + b }\n")
	write(t, filepath.Join(dir, "calc_test.go"), "package calc\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {}\n")
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
	static, err := scope.StaticChecks()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(static, "静态检查（宿主）") || !strings.Contains(static, "通过语法解析、gofmt 和 go vet") {
		t.Fatalf("静态检查结果异常:\n%s", static)
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
	outside := t.TempDir()
	write(t, filepath.Join(outside, "escape.go"), "package escape\n")
	if err := os.Symlink(filepath.Join(outside, "escape.go"), filepath.Join(dir, "escape.go")); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "git", "add", "escape.go")
	run(t, dir, "git", "commit", "-m", "link")
	linked, err := scope.StaticChecks()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(linked, "escape.go: 通过") || !strings.Contains(linked, "路径校验失败") {
		t.Fatalf("静态检查跟随了检出外链接:\n%s", linked)
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

// 多包改动：旧实现把不同包的文件拍平进同一个临时 module，go vet 会直接报
// found packages；现在按包目录在检出内校验。
func TestStaticChecksVetPerPackage(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, "git", "init", "-b", "main")
	run(t, dir, "git", "config", "user.email", "test@example.com")
	run(t, dir, "git", "config", "user.name", "test")
	write(t, filepath.Join(dir, "go.mod"), "module example.com/multi\n\ngo 1.25.0\n")
	write(t, filepath.Join(dir, "alpha", "alpha.go"), "package alpha\n\nfunc Name() string { return helper() }\n")
	write(t, filepath.Join(dir, "alpha", "helper.go"), "package alpha\n\nfunc helper() string { return \"a\" }\n")
	write(t, filepath.Join(dir, "beta", "beta.go"), "package beta\n\nfunc Name() string { return \"b\" }\n")
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-m", "base")
	base := strings.TrimSpace(run(t, dir, "git", "rev-parse", "HEAD"))
	// 两个包各改一个文件；alpha 依赖同包中未改动的 helper.go。
	write(t, filepath.Join(dir, "alpha", "alpha.go"), "package alpha\n\nfunc Name() string { return helper() + \"!\" }\n")
	write(t, filepath.Join(dir, "beta", "beta.go"), "package beta\n\nfunc Name() string { return \"B\" }\n")
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-m", "head")
	run(t, dir, "git", "update-ref", baseRef, base)
	run(t, dir, "git", "update-ref", mergeBaseRef, base)

	static, err := (ToolScope{Dir: dir}).StaticChecks()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(static, "通过语法解析、gofmt 和 go vet") {
		t.Fatalf("多包改动应通过包维度 vet:\n%s", static)
	}
	for _, bad := range []string{"found packages", "undefined:"} {
		if strings.Contains(static, bad) {
			t.Fatalf("仍然出现拍平带来的假报错 %q:\n%s", bad, static)
		}
	}
}

// 宿主缺依赖不是代码缺陷：必须标成「未完成」，不能当发现发到 PR。
func TestStaticChecksMissingDependencyIsNotAFinding(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, "git", "init", "-b", "main")
	run(t, dir, "git", "config", "user.email", "test@example.com")
	run(t, dir, "git", "config", "user.name", "test")
	write(t, filepath.Join(dir, "go.mod"), "module example.com/needs\n\ngo 1.25.0\n")
	write(t, filepath.Join(dir, "needs.go"), "package needs\n")
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-m", "base")
	base := strings.TrimSpace(run(t, dir, "git", "rev-parse", "HEAD"))
	write(t, filepath.Join(dir, "needs.go"), "package needs\n\nimport \"example.com/mojo-missing-dep\"\n\nfunc Use() int { return mojo.Value() }\n")
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-m", "head")
	run(t, dir, "git", "update-ref", baseRef, base)
	run(t, dir, "git", "update-ref", mergeBaseRef, base)

	static, err := (ToolScope{Dir: dir}).StaticChecks()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(static, "go vet 未完成") {
		t.Fatalf("缺依赖应标为未完成:\n%s", static)
	}
	if strings.Contains(static, "go vet: ") {
		t.Fatalf("把宿主环境问题当成了缺陷:\n%s", static)
	}
	if !strings.Contains(static, "通过语法解析和 gofmt") {
		t.Fatalf("未完成的 vet 不应伪装成全部通过:\n%s", static)
	}
}

// 真正的 vet 问题仍然要当成发现报出来，不能被「宿主未完成」吞掉。
func TestStaticChecksReportsRealVetFinding(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, "git", "init", "-b", "main")
	run(t, dir, "git", "config", "user.email", "test@example.com")
	run(t, dir, "git", "config", "user.name", "test")
	write(t, filepath.Join(dir, "go.mod"), "module example.com/bad\n\ngo 1.25.0\n")
	write(t, filepath.Join(dir, "bad.go"), "package bad\n")
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-m", "base")
	base := strings.TrimSpace(run(t, dir, "git", "rev-parse", "HEAD"))
	write(t, dir+"/bad.go", "package bad\n\nimport \"fmt\"\n\nfunc F() error { return fmt.Errorf(\"%d\", \"not a number\") }\n")
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-m", "head")
	run(t, dir, "git", "update-ref", baseRef, base)
	run(t, dir, "git", "update-ref", mergeBaseRef, base)

	static, err := (ToolScope{Dir: dir}).StaticChecks()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(static, "go vet: ") || !strings.Contains(static, "bad.go") {
		t.Fatalf("真正的 vet 问题没有报出来:\n%s", static)
	}
	if strings.Contains(static, "go vet 未完成") {
		t.Fatalf("把真实 finding 归成了宿主未完成:\n%s", static)
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
	if !strings.HasPrefix(got, BotPrefix) || !strings.Contains(got, "calc.go:4") || !strings.Contains(got, "建议\n无") {
		t.Fatalf("评论格式异常:\n%s", got)
	}
}

func TestNotesRenderIncludesEvidenceSource(t *testing.T) {
	notes := &Notes{}
	if err := notes.AddSource("静态", "建议", "go.mod", 3, "依赖版本没有锁定"); err != nil {
		t.Fatal(err)
	}
	if got := notes.Render(); !strings.Contains(got, "[静态]") || !strings.Contains(got, "go.mod:3") {
		t.Fatalf("source/path missing: %s", got)
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
