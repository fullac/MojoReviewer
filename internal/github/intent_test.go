package github

import (
	"strings"
	"testing"
)

func TestParseIntent(t *testing.T) {
	cases := []struct {
		body, trigger string
		want          Intent
		restContains  string
		restExact     string
	}{
		{"@mojo", "mojo", IntentReview, "", ""},
		{"@mojo 再看一下空指针", "mojo", IntentReview, "空指针", "再看一下空指针"},
		{"@mojo review focus on auth", "mojo", IntentReview, "", "focus on auth"},
		{"@mojo fix 把超时改成可配置", "mojo", IntentFix, "", "把超时改成可配置"},
		{"@Mojo 修复：空指针", "mojo", IntentFix, "", "空指针"},
		{"@mojo remember 这个仓库比较基线用 refs/review/base", "mojo", IntentRemember, "", "这个仓库比较基线用 refs/review/base"},
		{"@mojo 记住：禁止 push main", "mojo", IntentRemember, "", "禁止 push main"},
		{"请 @mojo 看看登录逻辑", "mojo", IntentReview, "登录逻辑", ""},
		{"@mojo patch timeout", "mojo", IntentFix, "", "timeout"},
		{"@mojo memo keep base ref pinned", "mojo", IntentRemember, "", "keep base ref pinned"},
	}
	for _, tc := range cases {
		got, rest := ParseIntent(tc.body, tc.trigger)
		if got != tc.want {
			t.Fatalf("%q intent=%s want %s (rest=%q)", tc.body, got, tc.want, rest)
		}
		if tc.restExact != "" && rest != tc.restExact {
			t.Fatalf("%q rest=%q want exact %q", tc.body, rest, tc.restExact)
		}
		if tc.restContains != "" && !strings.Contains(rest, tc.restContains) {
			t.Fatalf("%q rest=%q missing %q", tc.body, rest, tc.restContains)
		}
	}
}
