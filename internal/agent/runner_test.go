package agent

import (
	"strings"
	"testing"

	"github.com/mojoreviewer/mojoreviewer/internal/github"
)

func TestPromptAvoidsShallowBranchTripleDot(t *testing.T) {
	got := prompt(ReviewInput{
		Review:  github.Review{Repo: "acme/web", Number: 1, BaseBranch: "main", BaseSHA: "base", HeadSHA: "head"},
		WorkDir: "/tmp/review",
		Diff:    "diff",
	})
	for _, want := range []string{"detached checkout", "shallow", "git diff main...HEAD", "refs/review/base"} {
		if !strings.Contains(got, want) {
			t.Fatalf("提示缺少 %q:\n%s", want, got)
		}
	}
}
