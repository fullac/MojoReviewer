// 验证批次计划覆盖全部文件，且不会依赖可能截断的提示词清单。
package review

import (
	"context"
	"fmt"
	"path/filepath"
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
