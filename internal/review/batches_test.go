// 验证批次计划覆盖全部文件，且不会依赖可能截断的提示词清单。
package review

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlanBatchesCompleteAndStable(t *testing.T) {
	dir := symlinkReviewRepo(t)
	for i := 0; i < 43; i++ {
		write(t, filepath.Join(dir, fmt.Sprintf("pkg/f%02d.go", i)), "package pkg\n")
	}
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-m", "head")
	batches, err := PlanBatches(context.Background(), dir)
	if err != nil || len(batches) != 3 {
		t.Fatalf("%v %v", batches, err)
	}
	seen := map[string]bool{}
	for i, b := range batches {
		if b.Index != i+1 || b.Total != 3 || len(b.Files) > BatchMaxFiles {
			t.Fatal(b)
		}
		for _, p := range b.Files {
			if seen[p] {
				t.Fatal("duplicate", p)
			}
			seen[p] = true
		}
	}
	if len(seen) != 43 {
		t.Fatal("missing files", len(seen))
	}
}

func TestBatchManifestKeepsSizeAndCompletenessContract(t *testing.T) {
	if BatchManifest(nil, Batch{Files: []string{"x"}}) != nil {
		t.Fatal("disabled manifest restored")
	}
	m := &Manifest{}
	b := Batch{}
	for i := 0; i < 20; i++ {
		path := fmt.Sprint(i)
		b.Files = append(b.Files, path)
		m.Files = append(m.Files, ChangedFile{Path: path, Tests: []string{strings.Repeat("a", 2000)}})
	}
	out := BatchManifest(m, b)
	raw, _ := json.Marshal(out)
	if len(raw) > ManifestMaxBytes || out.Complete || out.OmittedFiles != len(b.Files)-len(out.Files) {
		t.Fatalf("%d %+v", len(raw), out)
	}
}
