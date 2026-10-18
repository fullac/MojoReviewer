package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mojoreviewer/mojoreviewer/internal/agent"
	"github.com/mojoreviewer/mojoreviewer/internal/github"
	"github.com/mojoreviewer/mojoreviewer/internal/review"
	"github.com/mojoreviewer/mojoreviewer/internal/store"
)

func localReviewItem(t *testing.T) github.Review {
	t.Helper()
	origin := t.TempDir()
	runGit(t, origin, "init", "-b", "main")
	runGit(t, origin, "config", "user.name", "test")
	runGit(t, origin, "config", "user.email", "test@example.com")
	runGit(t, origin, "commit", "--allow-empty", "-m", "fixture")
	head := runGit(t, origin, "rev-parse", "HEAD")
	return github.Review{
		Repo: "acme/web", Number: 7, SessionKey: "acme/web#7", AttemptID: "interrupted-attempt",
		CloneURL: "file://" + origin, BaseBranch: "main", BaseSHA: head, HeadSHA: head,
	}
}

func TestModelInterruptionPublishesFailureReceipt(t *testing.T) {
	for _, withNotes := range []bool{false, true} {
		t.Run(map[bool]string{false: "without notes", true: "with notes"}[withNotes], func(t *testing.T) {
			s, runner, _, commentPath := testServer(t, "", "")
			s.cfg.GitHub.Host = "ghe.example.com"
			url := "https://ghe.example.com/acme/web/pull/7#issuecomment-1"
			t.Setenv("MOJO_TEST_COMMENT_URL", url)
			var notes review.Notes
			if err := notes.Add("严重", "a.go", 7, "缺少错误检查"); err != nil {
				t.Fatal(err)
			}
			runner.reviewResult = &agent.ReviewResult{SessionID: "existing-session", Text: "不应使用的自由文本 private-test-value"}
			if withNotes {
				runner.reviewResult.Noted = true
				runner.reviewResult.Text = notes.Render()
			}
			runner.reviewErr = &agent.ReviewFailure{
				Reason: "重复读取未产生新证据，运行时已中断审查。", TaskID: "tq-7",
				Coverage: "读取回执：patch 1 个文件；已跳过重复读取 3 次。\n- a.go",
			}
			s.runOne(context.Background(), localReviewItem(t))
			body, err := os.ReadFile(commentPath)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"GH_HOST=ghe.example.com", review.BotPrefix, "审查未完成", "失败阶段：model", "tq-7", "继续前台任务 tq-7", "读取回执", "a.go"} {
				if !strings.Contains(string(body), want) {
					t.Fatalf("失败说明缺少 %q: %s", want, body)
				}
			}
			if strings.Contains(string(body), "private-test-value") || strings.Contains(string(body), "审查完成") {
				t.Fatalf("把失败当成成功，或泄露自由文本: %s", body)
			}
			if withNotes && !strings.Contains(string(body), "缺少错误检查") {
				t.Fatal("中断前的意见没有发布")
			}
			if !withNotes && !strings.Contains(string(body), "这不表示没有问题") {
				t.Fatal("没有意见时不应宣称审查通过")
			}
			raw, err := os.ReadFile(filepath.Join(s.cfg.DataDir, "activity.json"))
			if err != nil {
				t.Fatal(err)
			}
			var rows []store.Activity
			if err := json.Unmarshal(raw, &rows); err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1 || rows[0].State != "failed" || rows[0].Stage != "model" || rows[0].CommentURL != url {
				t.Fatalf("失败评论应留回执，但审查仍失败: %s", raw)
			}
			if rec, ok := s.store.Get("acme/web#7"); !ok || rec.SessionID != "existing-session" {
				t.Fatal("可恢复会话未保留")
			}
			if len(s.pending) != 0 {
				t.Fatal("失败后没有释放 pending")
			}
		})
	}
}

func TestFailureCommentCannotBePublished(t *testing.T) {
	s, runner, _, commentPath := testServer(t, "", "")
	t.Setenv("MOJO_TEST_POST_FAILURE", "1")
	runner.reviewResult = &agent.ReviewResult{SessionID: "recoverable"}
	runner.reviewErr = errors.New("raw prompt private-test-value")
	s.runOne(context.Background(), localReviewItem(t))
	if _, err := os.Stat(commentPath); !os.IsNotExist(err) {
		t.Fatal("模拟发布失败却生成了发布记录")
	}
	raw, err := os.ReadFile(filepath.Join(s.cfg.DataDir, "activity.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rows []store.Activity
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].State != "failed" || rows[0].CommentURL != "" || strings.Contains(string(raw), "private-test-value") {
		t.Fatalf("发布失败不能伪造成功回执: %s", raw)
	}
}

func TestGenericModelErrorIsNotPublishedRaw(t *testing.T) {
	body := reviewFailureComment(agent.ReviewResult{Text: "private-model-output"}, errors.New("raw prompt private-test-value"))
	if !strings.Contains(body, "模型执行失败") || strings.Contains(body, "private-") {
		t.Fatalf("原始错误或模型文本泄漏: %s", body)
	}
}
