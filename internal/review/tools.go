package review

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	maxStatBytes  = 40_000
	maxFileDiff   = 60_000
	maxReadBytes  = 60_000
	maxGrepBytes  = 40_000
	maxSymbolHits = 80
	maxTestHits   = 40
	maxReadLines  = 400
)

// ToolScope 是一次审查的只读范围。所有路径都必须落在 Dir 内。
type ToolScope struct {
	Dir string
}

// StaticChecks performs bounded, read-only host checks on changed Go files.
// It never executes repository code and returns evidence for the model.
func (s ToolScope) StaticChecks() (string, error) {
	if err := s.require(); err != nil {
		return "", err
	}
	out, err := gitOutput(s.Dir, "diff", "--name-only", "-z", mergeBaseRef, "HEAD")
	if err != nil {
		return "", err
	}
	var lines []string
	for _, rel := range strings.Split(out, "\x00") {
		rel = strings.TrimSpace(rel)
		if rel == "" || !strings.HasSuffix(strings.ToLower(rel), ".go") {
			continue
		}
		clean, err := s.clean(rel)
		if err != nil {
			lines = append(lines, rel+": 路径校验失败")
			continue
		}
		abs := filepath.Join(s.Dir, filepath.FromSlash(clean))
		raw, err := os.ReadFile(abs)
		if err != nil {
			// Deleted files are covered by the patch and need no source parse.
			continue
		}
		fset := token.NewFileSet()
		if _, err := parser.ParseFile(fset, clean, raw, parser.AllErrors); err != nil {
			lines = append(lines, fmt.Sprintf("%s: Go 语法错误：%s", clean, truncate(err.Error(), 240)))
			continue
		}
		formatted, err := format.Source(raw)
		if err != nil {
			lines = append(lines, fmt.Sprintf("%s: gofmt 无法格式化：%s", clean, truncate(err.Error(), 240)))
			continue
		}
		if !bytes.Equal(formatted, raw) {
			lines = append(lines, clean+": 未通过 gofmt")
		}
	}
	if len(lines) == 0 {
		return "静态检查（宿主）：改动的 Go 文件通过语法解析和 gofmt；没有执行仓库代码。", nil
	}
	return clip("静态检查（宿主）：\n- "+strings.Join(lines, "\n- ")+"\n未执行仓库代码。", 8000), nil
}

// Stat 返回文件地图的第一页。继续读取请使用 StatPage。
func (s ToolScope) Stat() (string, error) {
	return s.StatPage(0, 50)
}

// StatPage 按文件分页；has_more 与 patch_truncated 分别表示列表和 patch 的状态。
func (s ToolScope) StatPage(offset, limit int) (string, error) {
	if err := s.require(); err != nil {
		return "", err
	}
	if offset < 0 || limit < 0 {
		return "", fmt.Errorf("offset/limit 不能为负数")
	}
	if limit == 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	out, err := gitOutput(s.Dir, "diff", "--numstat", "--no-renames", "-z", mergeBaseRef, "HEAD")
	if err != nil {
		return "", err
	}
	patch, patchErr := gitOutput(s.Dir, "diff", "--patch", mergeBaseRef, "HEAD")
	if patchErr != nil {
		return "", patchErr
	}
	var rows []string
	for _, record := range strings.Split(out, "\x00") {
		fields := strings.SplitN(record, "\t", 3)
		if len(fields) == 3 {
			// Quote 控制字符，保证一个文件只占一行；带空格的路径保留完整。
			path := fields[2]
			if strings.ContainsAny(path, "\n\r\t") {
				path = strconv.Quote(path)
			}
			rows = append(rows, fmt.Sprintf("%s\t+%s\t-%s\n", path, fields[0], fields[1]))
		}
	}
	if offset > len(rows) {
		offset = len(rows)
	}
	var page strings.Builder
	next := offset
	for next < len(rows) && next-offset < limit && page.Len()+len(rows[next]) <= maxStatBytes-512 {
		page.WriteString(rows[next])
		next++
	}
	if next == offset && next < len(rows) {
		return "", fmt.Errorf("单条文件记录超过输出上限")
	}
	hasMore := next < len(rows)
	nextOffset := -1
	if hasMore {
		nextOffset = next
	}
	header := fmt.Sprintf("patch_truncated: %t\ntotal_files: %d\noffset: %d\nhas_more: %t\nnext_offset: %d\n", len(patch) > maxDiffBytes, len(rows), offset, hasMore, nextOffset)
	if len(rows) == 0 {
		return header + "（没有代码差异）", nil
	}
	return header + strings.TrimSuffix(page.String(), "\n"), nil
}

// FileDiff 只返回一个文件相对 refs/review/merge-base 的 patch。
func (s ToolScope) FileDiff(path string) (string, error) {
	rel, err := s.clean(path)
	if err != nil {
		return "", err
	}
	out, err := gitOutput(s.Dir, "diff", "--patch", mergeBaseRef, "HEAD", "--", rel)
	if err != nil {
		return "", err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return fmt.Sprintf("（%s 相对 %s 没有差异）", rel, mergeBaseRef), nil
	}
	return clip(out, maxFileDiff), nil
}

// ReadFile 读取检出内的文件。start/end 是 1-based 行号，end 为 0 表示读到上限。
func (s ToolScope) ReadFile(path string, start, end int) (string, error) {
	rel, err := s.clean(path)
	if err != nil {
		return "", err
	}
	abs := filepath.Join(s.Dir, rel)
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("读取 %s: %w", rel, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s 是目录", rel)
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	lines := strings.Split(string(raw), "\n")
	if start <= 0 {
		start = 1
	}
	if start > len(lines) {
		return fmt.Sprintf("（%s 共 %d 行，起始行 %d 超出范围）", rel, len(lines), start), nil
	}
	if end <= 0 || end > len(lines) {
		end = len(lines)
	}
	if end < start {
		return "", fmt.Errorf("行号范围无效: %d-%d", start, end)
	}
	if end-start+1 > maxReadLines {
		end = start + maxReadLines - 1
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s:%d-%d\n", rel, start, end)
	for i := start; i <= end; i++ {
		fmt.Fprintf(&b, "%d\t%s\n", i, lines[i-1])
	}
	return clip(b.String(), maxReadBytes), nil
}

// Grep 只在本次检出里搜索。glob 为空时搜索全部文本文件。
func (s ToolScope) Grep(pattern, glob string) (string, error) {
	if err := s.require(); err != nil {
		return "", err
	}
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return "", fmt.Errorf("pattern 为空")
	}
	if _, err := regexp.Compile(pattern); err != nil {
		return "", fmt.Errorf("pattern 无效: %w", err)
	}
	pathspec, err := grepPathspec(glob)
	if err != nil {
		return "", err
	}
	args := []string{"grep", "-n", "-I", "-E", "--", pattern}
	if pathspec != "" {
		args = append(args, "--", pathspec)
	}
	cmd := exec.Command("git", append([]string{"-C", s.Dir}, args...)...)
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return "（没有匹配）", nil
		}
		return "", fmt.Errorf("grep 失败: %w: %s", err, truncate(text, 300))
	}
	if text == "" {
		return "（没有匹配）", nil
	}
	return clip(text, maxGrepBytes), nil
}

// ChangedSymbols 从本次 patch 里抽出改动的函数、类型、方法名。
func (s ToolScope) ChangedSymbols() (string, error) {
	if err := s.require(); err != nil {
		return "", err
	}
	names, err := gitOutput(s.Dir, "diff", "--name-only", "--diff-filter=ACMR", mergeBaseRef, "HEAD")
	if err != nil {
		return "", err
	}
	var hits []string
	for _, rel := range splitLines(names) {
		rel, err := s.clean(rel)
		if err != nil {
			continue
		}
		hunks, err := changedLines(s.Dir, rel)
		if err != nil || len(hunks) == 0 {
			continue
		}
		hits = append(hits, symbolsIn(filepath.Join(s.Dir, rel), rel, hunks)...)
		if len(hits) >= maxSymbolHits {
			hits = hits[:maxSymbolHits]
			break
		}
	}
	if len(hits) == 0 {
		return "（没有识别到改动的符号）", nil
	}
	return strings.Join(hits, "\n"), nil
}

// TestsAround 列出与改动文件同目录、父目录或测试命名对应的测试文件。
// path 为空时，用本次 diff 的全部改动文件做种子。
func (s ToolScope) TestsAround(path string) (string, error) {
	if err := s.require(); err != nil {
		return "", err
	}
	var seeds []string
	if strings.TrimSpace(path) != "" {
		rel, err := s.clean(path)
		if err != nil {
			return "", err
		}
		seeds = append(seeds, rel)
	}
	changed, err := gitOutput(s.Dir, "diff", "--name-only", "--diff-filter=ACMR", mergeBaseRef, "HEAD")
	if err != nil {
		return "", err
	}
	seeds = append(seeds, splitLines(changed)...)
	seen := map[string]bool{}
	var hits []string
	for _, seed := range seeds {
		seed, err := s.clean(seed)
		if err != nil {
			continue
		}
		for _, candidate := range testCandidates(s.Dir, seed) {
			if seen[candidate] {
				continue
			}
			candidate, err = s.clean(candidate)
			if err != nil {
				continue
			}
			info, err := os.Stat(filepath.Join(s.Dir, candidate))
			if err != nil || info.IsDir() {
				continue
			}
			seen[candidate] = true
			hits = append(hits, candidate)
			if len(hits) >= maxTestHits {
				break
			}
		}
	}
	if len(hits) == 0 {
		return "（改动附近没有找到测试文件）", nil
	}
	sort.Strings(hits)
	return strings.Join(hits, "\n"), nil
}

func grepPathspec(glob string) (string, error) {
	glob = strings.TrimSpace(glob)
	if glob == "" {
		return "", nil
	}
	if strings.Contains(glob, "..") || strings.ContainsAny(glob, "\n\r") {
		return "", fmt.Errorf("glob 无效")
	}
	// git 2.43 的 grep 没有 --glob。把 *.go 收成限定在检出内的 pathspec。
	return ":/" + strings.TrimPrefix(glob, "/"), nil
}

func (s ToolScope) require() error {
	if strings.TrimSpace(s.Dir) == "" {
		return fmt.Errorf("审查目录为空")
	}
	info, err := os.Stat(s.Dir)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("审查目录不可用: %s", s.Dir)
	}
	return nil
}

// clean 把路径收成检出内的相对路径，同时拒绝通过符号链接逃出检出。
// 文件已删除时检查最近的已有父目录，仍允许读取它的 Git patch。
func (s ToolScope) clean(path string) (string, error) {
	if err := s.require(); err != nil {
		return "", err
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("path 为空")
	}
	root, err := filepath.Abs(s.Dir)
	if err != nil {
		return "", err
	}
	var abs string
	if filepath.IsAbs(path) {
		abs = filepath.Clean(path)
	} else {
		abs = filepath.Clean(filepath.Join(root, path))
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("路径超出本次检出: %s", path)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("无法解析审查目录")
	}
	existing := abs
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		} else if !os.IsNotExist(err) || existing == root {
			return "", fmt.Errorf("无法检查检出内路径: %s", path)
		}
		existing = filepath.Dir(existing)
	}
	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", fmt.Errorf("无法解析检出内路径: %s", path)
	}
	realRel, err := filepath.Rel(realRoot, resolved)
	if err != nil || realRel == ".." || strings.HasPrefix(realRel, ".."+string(filepath.Separator)) || filepath.IsAbs(realRel) {
		return "", fmt.Errorf("符号链接超出本次检出: %s", path)
	}
	return rel, nil
}

func changedLines(dir, rel string) ([][2]int, error) {
	out, err := gitOutput(dir, "diff", "-U0", mergeBaseRef, "HEAD", "--", rel)
	if err != nil {
		return nil, err
	}
	var hunks [][2]int
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "@@") {
			continue
		}
		plus := strings.Index(line, "+")
		if plus < 0 {
			continue
		}
		rest := line[plus+1:]
		space := strings.Index(rest, " ")
		if space < 0 {
			continue
		}
		span := rest[:space]
		start, count := 1, 1
		if n, after, ok := strings.Cut(span, ","); ok {
			start, _ = strconv.Atoi(n)
			count, _ = strconv.Atoi(after)
		} else {
			start, _ = strconv.Atoi(span)
		}
		if count <= 0 {
			continue
		}
		if start <= 0 {
			start = 1
		}
		hunks = append(hunks, [2]int{start, start + count - 1})
	}
	return hunks, nil
}

func symbolsIn(abs, rel string, hunks [][2]int) []string {
	if strings.HasSuffix(rel, ".go") {
		if hits := goSymbols(abs, rel, hunks); len(hits) > 0 {
			return hits
		}
	}
	return regexSymbols(abs, rel, hunks)
}

func goSymbols(abs, rel string, hunks [][2]int) []string {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, abs, nil, 0)
	if err != nil {
		return nil
	}
	var hits []string
	ast.Inspect(file, func(n ast.Node) bool {
		var name string
		switch node := n.(type) {
		case *ast.FuncDecl:
			name = node.Name.Name
			if node.Recv != nil && len(node.Recv.List) > 0 {
				name = recvName(node.Recv.List[0].Type) + "." + name
			}
		case *ast.TypeSpec:
			name = node.Name.Name
		default:
			return true
		}
		start := fset.Position(n.Pos()).Line
		end := fset.Position(n.End()).Line
		if overlaps(hunks, start, end) {
			hits = append(hits, fmt.Sprintf("%s\t%s\t%d", rel, name, start))
		}
		return true
	})
	return hits
}

func recvName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return recvName(t.X)
	case *ast.Ident:
		return t.Name
	default:
		return "?"
	}
}

func overlaps(hunks [][2]int, start, end int) bool {
	for _, h := range hunks {
		if start <= h[1] && end >= h[0] {
			return true
		}
	}
	return false
}

var symbolRE = regexp.MustCompile(`(?m)^(?:func|function|class|type|struct|interface|def|fn)\s+(\w+(?:\.\w+)?)`)

func regexSymbols(abs, rel string, hunks [][2]int) []string {
	raw, err := os.ReadFile(abs)
	if err != nil {
		return nil
	}
	lines := strings.Split(string(raw), "\n")
	var hits []string
	for i, line := range lines {
		m := symbolRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if overlaps(hunks, i+1, i+1) {
			hits = append(hits, fmt.Sprintf("%s\t%s\t%d", rel, m[1], i+1))
		}
	}
	return hits
}

func testCandidates(root, rel string) []string {
	dir := filepath.Dir(rel)
	base := filepath.Base(rel)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	var out []string
	add := func(path string) {
		path = filepath.ToSlash(path)
		if path != "" && path != "." {
			out = append(out, path)
		}
	}
	if isTestName(stem) {
		add(rel)
	}
	switch ext {
	case ".go":
		add(filepath.Join(dir, strings.TrimSuffix(stem, "_test")+"_test.go"))
	case ".py":
		add(filepath.Join(dir, "test_"+strings.TrimPrefix(stem, "test_")+".py"))
	case ".js", ".ts", ".jsx", ".tsx":
		add(filepath.Join(dir, stem+".test"+ext))
		add(filepath.Join(dir, stem+".spec"+ext))
	}
	for _, name := range []string{"*_test.go", "*_test.py", "test_*.py", "*.test.*", "*.spec.*"} {
		for _, searchDir := range []string{dir, filepath.Dir(dir)} {
			if searchDir == "" || searchDir == "." && dir == "." && searchDir != dir {
				continue
			}
			matches, _ := filepath.Glob(filepath.Join(root, searchDir, name))
			for _, m := range matches {
				if relPath, err := filepath.Rel(root, m); err == nil {
					add(relPath)
				}
			}
		}
	}
	return out
}

func isTestName(stem string) bool {
	return strings.HasSuffix(stem, "_test") || strings.HasPrefix(stem, "test_") || strings.HasSuffix(stem, ".test") || strings.HasSuffix(stem, ".spec")
}

func splitLines(raw string) []string {
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s 失败: %w: %s", strings.Join(args, " "), err, truncate(strings.TrimSpace(stderr.String()), 300))
	}
	return string(out), nil
}

func clip(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "\n\n…输出过长，已截断。"
}
