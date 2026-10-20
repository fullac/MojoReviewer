package server

import (
	"strings"
	"testing"

	"github.com/mojoreviewer/mojoreviewer/internal/github"
)

func TestFixCommitTitleDoesNotPublishUntrustedText(t *testing.T) {
	secret := "ghp_" + strings.Repeat("x", 32)
	for _, instruction := range []string{"", "fix " + secret} {
		item := github.Review{
			Number:      42,
			Title:       secret,
			Instruction: instruction,
			Comment:     secret,
		}
		got := publishMessage(item, secret+"\nmodel explanation")
		if strings.Contains(got, secret) {
			t.Fatal("untrusted secret escaped through the commit title")
		}
		if got != "MojoReviewer fix: apply requested changes for PR #42" {
			t.Fatalf("unexpected host-generated commit title: %q", got)
		}
	}
}
