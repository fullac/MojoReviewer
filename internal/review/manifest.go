// 预计算有界改动清单供宿主注入提示词，复用路径校验与符号解析，不包含代码补丁。
package review

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const ManifestMaxBytes = 16 << 10
const manifestMaxFiles = 200
const manifestMaxSourceBytes = 256 << 10

type Manifest struct {
	TotalFiles   int           `json:"total_files"`
	OmittedFiles int           `json:"omitted_files"`
	Complete     bool          `json:"complete"`
	Files        []ChangedFile `json:"files"`
}

type ChangedFile struct {
	Path           string          `json:"path"`
	Added          int             `json:"added"`
	Deleted        int             `json:"deleted"`
	Binary         bool            `json:"binary,omitempty"`
	Risk           int             `json:"risk"`
	Symbols        []ChangedSymbol `json:"symbols,omitempty"`
	Tests          []string        `json:"tests,omitempty"`
	DetailsPartial bool            `json:"details_partial,omitempty"`
}

type ChangedSymbol struct {
	Name string `json:"name"`
	Line int    `json:"line"`
}

// BuildManifest 有独立的五秒预算。缺失详情不会变成“没有符号/测试”的断言。
func BuildManifest(ctx context.Context, dir string) (*Manifest, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	scope := ToolScope{Dir: dir}
	if err := scope.require(); err != nil {
		return nil, err
	}
	stat, err := manifestGit(ctx, dir, 4<<20, "diff", "--no-ext-diff", "--no-textconv", "--numstat", "--no-renames", "-z", mergeBaseRef, "HEAD")
	if err != nil {
		return nil, err
	}
	var files []ChangedFile
	for _, record := range strings.Split(stat, "\x00") {
		if record == "" {
			continue
		}
		fields := strings.SplitN(record, "\t", 3)
		if len(fields) != 3 {
			return nil, fmt.Errorf("改动清单格式无效")
		}
		f := ChangedFile{Path: fields[2], Binary: fields[0] == "-"}
		if !f.Binary {
			f.Added, _ = strconv.Atoi(fields[0])
			f.Deleted, _ = strconv.Atoi(fields[1])
		}
		f.Risk = manifestRisk(f)
		files = append(files, f)
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].Risk != files[j].Risk {
			return files[i].Risk > files[j].Risk
		}
		return files[i].Path < files[j].Path
	})
	m := &Manifest{TotalFiles: len(files), Files: []ChangedFile{}}
	for _, f := range files {
		if len(m.Files) >= manifestMaxFiles || ctx.Err() != nil {
			break
		}
		enrichManifest(ctx, scope, &f)
		m.Files = append(m.Files, f)
		m.OmittedFiles = m.TotalFiles - len(m.Files)
		m.Complete = m.OmittedFiles == 0
		raw, _ := json.Marshal(m)
		if len(raw) > ManifestMaxBytes {
			m.Files = m.Files[:len(m.Files)-1]
			break
		}
	}
	m.OmittedFiles = m.TotalFiles - len(m.Files)
	m.Complete = m.OmittedFiles == 0
	return m, nil
}

func manifestRisk(f ChangedFile) int {
	score := 1
	if f.Added+f.Deleted > 100 {
		score += 2
	}
	if f.Added+f.Deleted > 500 {
		score += 2
	}
	lower := strings.ToLower(f.Path)
	for _, term := range []string{"auth", "permission", "security", "config", "migration"} {
		if strings.Contains(lower, term) {
			score += 2
			break
		}
	}
	if isTestName(strings.TrimSuffix(filepath.Base(lower), filepath.Ext(lower))) {
		score--
	}
	return score
}

func enrichManifest(ctx context.Context, scope ToolScope, f *ChangedFile) {
	rel, err := scope.clean(f.Path)
	if err != nil {
		f.DetailsPartial = true
		return
	}
	info, err := os.Lstat(filepath.Join(scope.Dir, rel))
	if err != nil || !info.Mode().IsRegular() || info.Size() > manifestMaxSourceBytes || f.Binary {
		f.DetailsPartial = true
		return
	}
	patch, err := manifestGit(ctx, scope.Dir, manifestMaxSourceBytes, "diff", "--no-ext-diff", "--no-textconv", "-U0", mergeBaseRef, "HEAD", "--", rel)
	if err != nil {
		f.DetailsPartial = true
		return
	}
	// Recheck real path immediately before source parsing.
	if _, err = scope.clean(rel); err != nil {
		f.DetailsPartial = true
		return
	}
	for _, hit := range symbolsIn(filepath.Join(scope.Dir, rel), rel, parseChangedLines(patch)) {
		parts := strings.SplitN(strings.TrimPrefix(hit, rel+"\t"), "\t", 2)
		if len(parts) != 2 {
			continue
		}
		if len(f.Symbols) >= 8 {
			f.DetailsPartial = true
			break
		}
		line, _ := strconv.Atoi(parts[1])
		f.Symbols = append(f.Symbols, ChangedSymbol{Name: parts[0], Line: line})
	}
	seen := map[string]bool{}
	for _, candidate := range testCandidates(scope.Dir, rel) {
		if ctx.Err() != nil {
			f.DetailsPartial = true
			break
		}
		candidate, err = scope.clean(candidate)
		if err != nil || seen[candidate] {
			continue
		}
		st, err := os.Lstat(filepath.Join(scope.Dir, candidate))
		if err != nil || !st.Mode().IsRegular() {
			continue
		}
		if len(f.Tests) >= 8 {
			f.DetailsPartial = true
			break
		}
		seen[candidate] = true
		f.Tests = append(f.Tests, filepath.ToSlash(candidate))
	}
	sort.Strings(f.Tests)
}

type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, io.ErrShortWrite
	}
	return b.Buffer.Write(p)
}

func manifestGit(ctx context.Context, dir string, limit int, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	out := &limitedBuffer{limit: limit}
	cmd.Stdout = out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("改动清单读取失败")
	}
	return out.String(), nil
}
