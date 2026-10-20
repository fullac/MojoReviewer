package projectmem

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// 默认会索引的根文件名（大小写不敏感匹配）。
var rootNames = []string{
	"readme.md",
	"readme.txt",
	"agents.md",
	"contributing.md",
	"architecture.md",
	"design.md",
	"security.md",
	"code_of_conduct.md",
	"claude.md",
	"soul.md",
	"aoci.txt",
	"changelog.md",
	"history.md",
}

var skipDirNames = map[string]struct{}{
	".git":          {},
	"node_modules":  {},
	"vendor":        {},
	"bin":           {},
	"dist":          {},
	"build":         {},
	".aoci":         {},
	".MojoReviewer": {},
	"testdata":      {},
}

// Candidate 是一次准备索引的仓库内相对路径。
type Candidate struct {
	Rel  string
	Size int64
}

// SelectKnowledgeFiles 从检出根目录挑高信号文档，不递归整仓代码。
// maxFiles / maxFileBytes 限制单次索引规模；<=0 时用默认值。
func SelectKnowledgeFiles(root string, maxFiles, maxFileBytes int) ([]Candidate, error) {
	if maxFiles <= 0 {
		maxFiles = 40
	}
	if maxFileBytes <= 0 {
		maxFileBytes = 64 << 10
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	var out []Candidate

	add := func(abs string) {
		if len(out) >= maxFiles {
			return
		}
		rel, err := filepath.Rel(root, abs)
		if err != nil {
			return
		}
		rel = filepath.ToSlash(rel)
		if rel == "." || strings.HasPrefix(rel, "../") {
			return
		}
		if shouldSkipPath(rel) {
			return
		}
		_, info, err := resolveKnowledgeFile(root, rel)
		if err != nil || info.Size() <= 0 || info.Size() > int64(maxFileBytes) {
			return
		}
		key := strings.ToLower(rel)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, Candidate{Rel: rel, Size: info.Size()})
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		name := strings.ToLower(ent.Name())
		for _, want := range rootNames {
			if name == want {
				add(filepath.Join(root, ent.Name()))
				break
			}
		}
	}

	// docs/ 下的 markdown，按路径排序后截断。
	docsDir := filepath.Join(root, "docs")
	if st, err := os.Stat(docsDir); err == nil && st.IsDir() {
		var docs []string
		_ = filepath.WalkDir(docsDir, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if d.IsDir() {
				if _, skip := skipDirNames[d.Name()]; skip {
					return filepath.SkipDir
				}
				return nil
			}
			ext := strings.ToLower(filepath.Ext(d.Name()))
			if ext != ".md" && ext != ".txt" {
				return nil
			}
			docs = append(docs, path)
			return nil
		})
		sort.Strings(docs)
		for _, path := range docs {
			if len(out) >= maxFiles {
				break
			}
			add(path)
		}
	}

	for _, rel := range []string{
		".github/CONTRIBUTING.md",
		".github/PULL_REQUEST_TEMPLATE.md",
		".github/pull_request_template.md",
	} {
		add(filepath.Join(root, filepath.FromSlash(rel)))
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Rel < out[j].Rel })
	if len(out) > maxFiles {
		out = out[:maxFiles]
	}
	return out, nil
}

// resolveKnowledgeFile is shared by selection and the final indexing read.
// Keep internal symlinks, but require their resolved target to be an ordinary
// file in this checkout and apply the sensitive-path exclusions to that target.
func resolveKnowledgeFile(root, rel string) (string, os.FileInfo, error) {
	if filepath.IsAbs(rel) {
		return "", nil, fmt.Errorf("项目文档路径必须相对检出")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", nil, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", nil, err
	}
	cleanRel := filepath.Clean(filepath.FromSlash(rel))
	if cleanRel == ".." || strings.HasPrefix(cleanRel, ".."+string(filepath.Separator)) || shouldSkipPath(cleanRel) {
		return "", nil, fmt.Errorf("项目文档路径不允许索引")
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(root, cleanRel))
	if err != nil {
		return "", nil, err
	}
	realRel, err := filepath.Rel(root, resolved)
	if err != nil || realRel == ".." || strings.HasPrefix(realRel, ".."+string(filepath.Separator)) || filepath.IsAbs(realRel) {
		return "", nil, fmt.Errorf("项目文档符号链接超出检出")
	}
	if shouldSkipPath(filepath.ToSlash(realRel)) {
		return "", nil, fmt.Errorf("项目文档目标不允许索引")
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", nil, err
	}
	if !info.Mode().IsRegular() {
		return "", nil, fmt.Errorf("项目文档不是普通文件")
	}
	return resolved, info, nil
}

func shouldSkipPath(rel string) bool {
	lower := strings.ToLower(rel)
	base := strings.ToLower(filepath.Base(rel))
	if strings.Contains(lower, "/") {
		parts := strings.Split(lower, "/")
		for _, p := range parts[:len(parts)-1] {
			if strings.HasPrefix(p, ".") && p != ".github" {
				return true
			}
		}
	}
	switch {
	case strings.HasPrefix(base, ".env"):
		return true
	case strings.Contains(base, "secret"), strings.Contains(base, "credential"), strings.Contains(base, "password"):
		return true
	case strings.HasSuffix(base, ".pem"), strings.HasSuffix(base, ".key"), strings.HasSuffix(base, ".p12"):
		return true
	case strings.HasSuffix(base, ".db"), strings.HasSuffix(base, ".sqlite"), strings.HasSuffix(base, ".sqlite3"):
		return true
	}
	return false
}
