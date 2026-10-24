// Verify start signals do not change review execution or duplicate handling.
package server

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestStartReactionDoesNotBlockReviewAndSkipsDuplicate(t *testing.T) {
	for _, fail := range []string{"", "1"} {
		t.Run("reaction_failure="+fail, func(t *testing.T) {
			s, runner, _, commentPath := testServer(t, "", "")
			t.Setenv("MOJO_TEST_REACTION_FAILURE", fail)
			item := localReviewItem(t)
			runner.onReview = func() {
				raw, err := os.ReadFile(os.Getenv("MOJO_TEST_REACTION"))
				if err != nil || !strings.Contains(string(raw), "repos/acme/web/issues/7/reactions") {
					t.Fatalf("start signal must precede model: %s %v", raw, err)
				}
			}
			s.runOne(context.Background(), item)
			if len(runner.inputs) != 1 {
				t.Fatal("reaction failure blocked review")
			}
			if _, err := os.Stat(commentPath); err != nil {
				t.Fatal("review result not published", err)
			}
			item.AttemptID = "duplicate"
			s.runOne(context.Background(), item)
			raw, err := os.ReadFile(os.Getenv("MOJO_TEST_REACTION"))
			if err != nil || strings.Count(string(raw), "content=eyes") != 1 || len(runner.inputs) != 1 {
				t.Fatal("duplicate head sent another reaction or ran model", string(raw), err)
			}
		})
	}
}
