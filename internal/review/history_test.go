// 验证未复核发现保留、身份跨行号稳定、状态变化和有界历史背景。
package review

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestFindingTransitions(t *testing.T) {
	n := Note{Severity: "建议", Title: "错误", Path: "a.go", Line: 1, Body: "- **问题描述：** bug\n\n- **代码位置：** `a.go:1`\n\n- **修复建议：** fix"}
	previous := Reconcile(nil, []Note{n}, "A")
	if len(previous) != 1 || previous[0].Status != "new" {
		t.Fatal(previous)
	}
	duplicates := Reconcile(nil, []Note{n, n}, "A")
	if len(duplicates) != 1 || duplicates[0].Status != "new" {
		t.Fatal("duplicate new findings became historical")
	}
	pending := Reconcile(previous, nil, "B")
	if pending[0].Status != "pending" || pending[0].VerifiedSHA != "A" {
		t.Fatal("absence resolved a finding")
	}
	n.Line = 2
	n.Body = strings.Replace(n.Body, "a.go:1", "a.go:2", 1)
	if FindingID(n) != previous[0].ID {
		t.Fatal("line shift changed identity")
	}
	n.FindingID = previous[0].ID
	n.Status = "persistent"
	present := Reconcile(previous, []Note{n}, "B")
	if present[0].Status != "persistent" {
		t.Fatal(present)
	}
	n.Status = "resolved"
	fixed := Reconcile(present, []Note{n}, "C")
	if fixed[0].Status != "resolved" || !strings.Contains(RenderChanges(fixed), "已解决") {
		t.Fatal(fixed)
	}
	n.Status = "persistent"
	conflict := Reconcile(previous, []Note{n, fixed[0].Note}, "C")
	if conflict[0].Status != "persistent" {
		t.Fatal("contradictory resolution won")
	}
	if !strings.Contains(RenderChanges(pending), "待确认") {
		t.Fatal("pending history hidden")
	}
}
func TestPreviousReviewBounded(t *testing.T) {
	dir := symlinkReviewRepo(t)
	sha := strings.TrimSpace(run(t, dir, "git", "rev-parse", "HEAD"))
	var findings []Finding
	for i := 0; i < 50; i++ {
		findings = append(findings, Finding{ID: "id", Status: "pending", Note: Note{Body: strings.Repeat("文", 500)}})
	}
	h := PreviousReview(context.Background(), dir, sha, findings)
	raw, _ := json.Marshal(h)
	if len(raw) > 16<<10 || h.Omitted == 0 || !h.DeltaComplete {
		t.Fatalf("unbounded %d %+v", len(raw), h)
	}
	h = PreviousReview(context.Background(), dir, strings.Repeat("f", 40), findings)
	if h.DeltaComplete {
		t.Fatal("missing old commit silently treated as no changes")
	}
}
