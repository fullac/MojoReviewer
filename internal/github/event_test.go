package github

import "testing"

func TestParsePullRequestSynchronize(t *testing.T) {
	body := []byte(`{
	  "action":"synchronize",
	  "pull_request":{"number":7,"title":"修复登录","body":"说明","draft":false,"user":{"login":"ada"},"html_url":"https://github.com/acme/web/pull/7","base":{"ref":"main","sha":"aaa"},"head":{"ref":"fix","sha":"bbb"}},
	  "repository":{"full_name":"acme/web","clone_url":"https://github.com/acme/web.git"}
	}`)
	got, err := ParseWebhook("pull_request", body, "", map[string]bool{"acme/web": true})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Number != 7 || got.HeadSHA != "bbb" || got.SessionKey != "acme/web#7" {
		t.Fatalf("解析结果错误: %+v", got)
	}
}

func TestParseIgnoresDraftAndOtherRepos(t *testing.T) {
	draft := []byte(`{"action":"opened","pull_request":{"number":1,"draft":true},"repository":{"full_name":"acme/web"}}`)
	got, err := ParseWebhook("pull_request", draft, "", nil)
	if err != nil || got != nil {
		t.Fatalf("草稿应忽略: %+v %v", got, err)
	}
	other := []byte(`{"action":"opened","pull_request":{"number":1,"draft":false,"base":{},"head":{}},"repository":{"full_name":"other/repo"}}`)
	got, err = ParseWebhook("pull_request", other, "", map[string]bool{"acme/web": true})
	if err != nil || got != nil {
		t.Fatalf("仓库不在名单内应忽略: %+v %v", got, err)
	}
}

func TestParseCommentRequiresMention(t *testing.T) {
	body := []byte(`{"action":"created","issue":{"number":7,"title":"修复登录","pull_request":{"url":"https://api.github.com/repos/acme/web/pulls/7"}},"comment":{"body":"@mojo 再看一下空指针","user":{"login":"ada"}},"repository":{"full_name":"acme/web","clone_url":"https://github.com/acme/web.git"}}`)
	got, err := ParseWebhook("issue_comment", body, "@mojo", nil)
	if err != nil || got == nil || got.Comment == "" {
		t.Fatalf("应触发评论审查: %+v %v", got, err)
	}
	self := []byte(`{"action":"created","issue":{"number":7,"pull_request":{"url":"x"}},"comment":{"body":"【MojoReviewer】 @mojo 已审查","user":{"login":"bot"}},"repository":{"full_name":"acme/web"}}`)
	got, err = ParseWebhook("issue_comment", self, "mojo", nil)
	if err != nil || got != nil {
		t.Fatalf("自己的评论应忽略: %+v %v", got, err)
	}
}

func TestValidSignature(t *testing.T) {
	body := []byte(`{"ok":true}`)
	secret := "topsecret"
	mac := sign(secret, body)
	if !ValidSignature(secret, body, "sha256="+mac) {
		t.Fatal("正确签名应通过")
	}
	if ValidSignature("", body, "sha256="+mac) || ValidSignature(secret, body, "sha256=00") {
		t.Fatal("空密钥或错误签名应拒绝")
	}
}
