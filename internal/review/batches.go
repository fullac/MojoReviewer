// 按目录和改动量生成完整批次计划，避免提示词清单截断导致文件被静默遗漏。
package review

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const BatchMaxFiles = 20
const BatchMaxLines = 2000

type Batch struct {
	Files []string `json:"files"`
	Index int      `json:"index"`
	Total int      `json:"total"`
}

func PlanBatches(ctx context.Context, dir string) ([]Batch, error) {
	raw, err := manifestGit(ctx, dir, 4<<20, "diff", "--numstat", "--no-renames", "-z", mergeBaseRef, "HEAD")
	if err != nil {
		return nil, fmt.Errorf("无法生成完整审查批次")
	}
	type row struct {
		path  string
		lines int
	}
	var rows []row
	total := 0
	for _, record := range strings.Split(raw, "\x00") {
		if record == "" {
			continue
		}
		f := strings.SplitN(record, "\t", 3)
		if len(f) != 3 {
			return nil, fmt.Errorf("批次文件记录无效")
		}
		a, _ := strconv.Atoi(f[0])
		d, _ := strconv.Atoi(f[1])
		rows = append(rows, row{f[2], a + d})
		total += a + d
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := filepath.Dir(rows[i].path), filepath.Dir(rows[j].path)
		if a != b {
			return a < b
		}
		return rows[i].path < rows[j].path
	})
	batches := []Batch{{Files: []string{}}}
	count := 0
	pathBytes := 0
	lastDir := ""
	split := len(rows) > BatchMaxFiles || total > BatchMaxLines
	for _, r := range rows {
		encoded, _ := json.Marshal(r.path)
		current := &batches[len(batches)-1]
		dir := filepath.Dir(r.path)
		if len(current.Files) > 0 && (pathBytes+len(encoded) > 8<<10 || (split && (len(current.Files) >= BatchMaxFiles || count+r.lines > BatchMaxLines || dir != lastDir))) {
			batches = append(batches, Batch{Files: []string{}})
			current = &batches[len(batches)-1]
			count = 0
			pathBytes = 0
		}
		current.Files = append(current.Files, r.path)
		count += r.lines
		pathBytes += len(encoded)
		lastDir = dir
	}
	for i := range batches {
		batches[i].Index = i + 1
		batches[i].Total = len(batches)
	}
	return batches, nil
}

func BatchManifest(m *Manifest, b Batch) *Manifest {
	if m == nil {
		return nil
	}
	out := &Manifest{TotalFiles: len(b.Files), Complete: true, Files: []ChangedFile{}}
	known := map[string]ChangedFile{}
	if m != nil {
		for _, f := range m.Files {
			known[f.Path] = f
		}
	}
	for _, path := range b.Files {
		if f, ok := known[path]; ok {
			out.Files = append(out.Files, f)
		} else {
			out.Files = append(out.Files, ChangedFile{Path: path, DetailsPartial: true})
		}
	}
	for {
		raw, _ := json.Marshal(out)
		if len(raw) <= ManifestMaxBytes || len(out.Files) == 0 {
			break
		}
		out.Files = out.Files[:len(out.Files)-1]
		out.OmittedFiles++
		out.Complete = false
	}
	return out
}
