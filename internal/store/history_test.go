// 验证自动去重与会话写入不覆盖已完成审查摘要，且读取不会泄露可变切片。
package store

import (
	"github.com/mojoreviewer/mojoreviewer/internal/review"
	"path/filepath"
	"testing"
)

func TestSessionAndReviewedHeadsRemainIndependent(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "reviews.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put("p", Record{Repo: "a/b", HeadSHA: "A", SessionID: "sessionA"}); err != nil {
		t.Fatal(err)
	}
	f := []review.Finding{{ID: "id", Status: "new", VerifiedSHA: "A"}}
	if err := s.SaveReviewed("p", "A", f); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReserveAutoHead("p", Record{Repo: "a/b", HeadSHA: "B"}); err != nil {
		t.Fatal(err)
	}
	r, _ := s.Get("p")
	if r.HeadSHA != "A" || r.ReviewedSHA != "A" || r.SessionID != "sessionA" {
		t.Fatal("reservation rewrote session", r)
	}
	s.Put("p", Record{HeadSHA: "B", SessionID: "sessionB"})
	r, _ = s.Get("p")
	if r.ReviewedSHA != "A" || len(r.Findings) != 1 {
		t.Fatal("session overwrote review history")
	}
	r.Findings[0].ID = "mutated"
	r, _ = s.Get("p")
	if r.Findings[0].ID != "id" {
		t.Fatal("mutable history exposed")
	}
}
