package agent

import (
	"path/filepath"
	"slices"
	"testing"

	taskstore "github.com/yurika0211/luckyagent-orchestrator/task"
)

func TestReviewPlanRecordsOrderedRoles(t *testing.T) {
	dir := t.TempDir()
	if err := recordReviewPlan(dir, "attempt-1", "session-1"); err != nil {
		t.Fatal(err)
	}
	store, err := taskstore.NewFileStore(filepath.Join(dir, "tasks", "session-1"))
	if err != nil {
		t.Fatal(err)
	}
	events, err := store.Events("attempt-1")
	if err != nil || len(events) != 1 || events[0].Mode != taskstore.ModePipeline {
		t.Fatalf("parent events=%+v err=%v", events, err)
	}
	children, err := store.List(taskstore.ListFilter{ParentID: "attempt-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != len(reviewRoles) {
		t.Fatalf("children=%d", len(children))
	}
	got := make([]string, 0, len(children))
	for _, child := range children {
		if child.ParentSessionID != "session-1" {
			t.Fatalf("child=%+v", child)
		}
		got = append(got, child.Role)
	}
	slices.Sort(got)
	want := make([]string, 0, len(reviewRoles))
	for _, role := range reviewRoles {
		want = append(want, role.name)
	}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("roles=%v", got)
	}
}
