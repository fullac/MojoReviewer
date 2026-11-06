package server

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/mojoreviewer/mojoreviewer/internal/agent"
	"github.com/mojoreviewer/mojoreviewer/internal/config"
	"github.com/mojoreviewer/mojoreviewer/internal/github"
	"github.com/mojoreviewer/mojoreviewer/internal/review"
	"github.com/mojoreviewer/mojoreviewer/internal/store"
)

type reviewRunner interface {
	Review(context.Context, agent.ReviewInput) (agent.ReviewResult, error)
	Fix(context.Context, agent.ReviewInput) (agent.ReviewResult, error)
	RememberProject(content string) error
	FixEnabled() bool
}

// Server 接收 GitHub webhook，串行跑只读审查。
type Server struct {
	cfg     config.Config
	runner  reviewRunner
	store   *store.Store
	allowed map[string]bool
	jobs    chan github.Review
	mu      sync.Mutex
	pending map[string]bool
}

// New 创建服务。jobs 缓冲满时 webhook 返回 429，不丢进无界队列。
func New(cfg config.Config, runner *agent.Runner, st *store.Store) *Server {
	allowed := map[string]bool{}
	for _, repo := range cfg.GitHub.Repos {
		allowed[repo] = true
	}
	return &Server{cfg: cfg, runner: runner, store: st, allowed: allowed, jobs: make(chan github.Review, 32), pending: map[string]bool{}}
}

// Start 启动 HTTP 服务和单个审查 worker。
func (s *Server) Start(ctx context.Context) error {
	if err := s.store.InterruptActivities(); err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("POST /webhook/github", s.handleWebhook)
	httpServer := &http.Server{Addr: s.cfg.Server.Addr(), Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	go s.worker(ctx)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	slog.Info("MojoReviewer 监听", "addr", s.cfg.Server.Addr())
	err := httpServer.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		http.Error(w, "读取请求失败", http.StatusBadRequest)
		return
	}
	if !github.ValidSignature(s.cfg.GitHub.WebhookSecret, body, r.Header.Get("X-Hub-Signature-256")) {
		http.Error(w, "签名无效", http.StatusUnauthorized)
		return
	}
	item, err := github.ParseWebhook(r.Header.Get("X-GitHub-Event"), body, s.cfg.GitHub.TriggerUser, s.allowed)
	if err != nil {
		http.Error(w, "事件无法解析", http.StatusBadRequest)
		return
	}
	if item == nil {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ignored"}`))
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if item.Comment == "" && s.pending[item.SessionKey+":"+item.HeadSHA] {
		slog.Debug("跳过排队中的重复审查", "pr", item.SessionKey, "head", item.HeadSHA)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"duplicate"}`))
		return
	}
	if item.Comment == "" && s.store.HasAutoHead(item.SessionKey, item.HeadSHA) {
		slog.Debug("跳过已尝试的自动审查", "pr", item.SessionKey, "head", item.HeadSHA)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"duplicate"}`))
		return
	}
	item.AttemptID = fmt.Sprint(time.Now().UnixNano())
	if delivery := r.Header.Get("X-GitHub-Delivery"); delivery != "" && len(delivery) <= 128 {
		item.AttemptID = "github:" + delivery
		seen, err := s.store.HasActivity(item.AttemptID)
		if err != nil {
			http.Error(w, "读取投递记录失败", 500)
			return
		}
		if seen {
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"status":"duplicate"}`))
			return
		}
	}
	if len(s.jobs) == cap(s.jobs) {
		http.Error(w, "队列已满", http.StatusTooManyRequests)
		return
	}

	if err := s.store.Activity(store.Activity{ID: item.AttemptID, Repo: item.Repo, Number: item.Number, HeadSHA: item.HeadSHA, State: "queued"}); err != nil {
		slog.Error("记录排队状态失败", "error", err)
		http.Error(w, "记录排队状态失败", http.StatusInternalServerError)
		return
	}
	if item.Comment == "" {
		s.pending[item.SessionKey+":"+item.HeadSHA] = true
	}
	s.jobs <- *item
	slog.Debug("任务已排队", "pr", item.SessionKey, "attempt", item.AttemptID)
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte(`{"status":"queued"}`))
}

func (s *Server) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case item := <-s.jobs:
			s.runOne(ctx, item)
		}
	}
}

func (s *Server) runOne(ctx context.Context, item github.Review) {
	if item.Intent == "" {
		item.Intent = github.IntentReview
	}
	slog.Info("开始任务", "pr", item.SessionKey, "intent", item.Intent)

	key := item.SessionKey + ":" + item.HeadSHA
	automatic := item.Comment == ""
	s.mu.Lock()
	id := item.AttemptID
	s.mu.Unlock()
	if id == "" {
		id = fmt.Sprint(time.Now().UnixNano())
	}
	activity := store.Activity{ID: id, Repo: item.Repo, Number: item.Number, HeadSHA: item.HeadSHA, State: "running"}
	defer func() {
		if activity.State == "running" {
			activity.State = "failed"
		}
		if err := s.store.Activity(activity); err != nil {
			slog.Error("记录审查状态失败", "error", err)
		}
		if automatic {
			s.mu.Lock()
			delete(s.pending, key)
			s.mu.Unlock()
		}
	}()
	if err := s.store.Activity(activity); err != nil {
		activity.Stage = "storage"
		slog.Error("记录运行状态失败", "error", err)
		return
	}
	// Once an automatic attempt actually starts, reserve its SHA even if setup fails.
	// Merely queued attempts remain recoverable after a process restart.
	if item.Comment == "" {
		reserved, err := s.store.ReserveAutoHead(item.SessionKey, store.Record{Repo: item.Repo, Number: item.Number, HeadSHA: item.HeadSHA})
		if err != nil {
			activity.Stage = "storage"
			slog.Error("记录自动审查提交失败", "error", err)
			return
		}
		if !reserved {
			activity.State, activity.Stage = "skipped", "duplicate"
			return
		}
	}
	slog.Info("开始审查", "pr", item.SessionKey)
	s.progress(id, store.ProgressEvent{Kind: "start", Summary: "审查开始"})
	if err := github.ReactStarted(ctx, item, s.cfg.GitHub.Host); err != nil {
		slog.Warn("添加开始表情失败，继续处理任务", "pr", item.SessionKey, "error", err)
	}

	if item.Comment != "" {
		activity.Stage = "metadata"
		resolved, err := github.ResolveComment(ctx, item, s.cfg.GitHub.Host)
		if err != nil {
			s.failProgress(id, "补齐 PR 信息失败", err)
			s.postFailure(item, "补齐 PR 信息失败", err)
			return
		}
		// ResolveComment 不改 Intent/Instruction/Comment。
		intent, instruction := item.Intent, item.Instruction
		comment := item.Comment
		item = resolved
		item.Intent = intent
		if item.Intent == "" {
			item.Intent = github.IntentReview
		}
		item.Instruction = instruction
		item.Comment = comment
	}
	activity.HeadSHA = item.HeadSHA
	activity.Stage = "checkout"

	switch item.Intent {
	case github.IntentRemember:
		s.runRemember(item, &activity)
		return
	case github.IntentFix:
		if !s.runner.FixEnabled() {
			s.runFixUnsupported(item, &activity)
			return
		}
		if !fixAuthorAllowed(s.cfg.Agent.FixAllowedAuthors, item.Author) {
			s.runFixAuthorDenied(item, &activity)
			return
		}
		s.runFix(ctx, item, &activity)
		return
	}
	ws, err := review.PrepareWithBase(s.cfg.DataDir, item.Repo, item.CloneURL, item.HeadSHA, item.SessionKey, item.BaseBranch, item.BaseSHA)
	if err != nil {
		s.failProgress(id, "准备仓库失败", err)
		s.postFailure(item, "准备仓库失败", err)
		return
	}
	s.progress(id, store.ProgressEvent{Kind: "checkout", Summary: "检出完成"})
	existing, _ := s.store.Get(item.SessionKey)
	activity.Stage = "model"
	// Diff is intentionally empty: the model pulls pr_diff_stat / pr_file_diff (issue #21).
	result, err := s.runner.Review(ctx, agent.ReviewInput{
		Review:    item,
		WorkDir:   ws.Dir,
		SessionID: existing.SessionID,
		AttemptID: id,
		DataDir:   s.cfg.DataDir,
	})
	storageFailed := false
	if result.SessionID != "" {
		if saveErr := s.store.Put(item.SessionKey, store.Record{Repo: item.Repo, Number: item.Number, HeadSHA: item.HeadSHA, SessionID: result.SessionID}); saveErr != nil {
			storageFailed = true
			activity.State, activity.Stage = "failed", "storage"
			slog.Error("保存审查会话失败", "pr", item.SessionKey, "error", saveErr)
		}
	}
	if err != nil {
		s.progress(id, store.ProgressEvent{Kind: "fail", Summary: "model：" + modelProgress(err)})
		slog.Warn("审查未完成，准备发布失败说明", "pr", item.SessionKey, "reason", agent.ModelFailureLog(err))
		commentURL, postErr := s.postComment(item, reviewFailureComment(result, err))
		if postErr != nil {
			slog.Error("发布审查失败说明失败", "pr", item.SessionKey, "error", postErr, "review_error", agent.ModelFailureLog(err))
			return
		}
		activity.CommentURL = commentURL
		// 保留 model/failed，不把“失败说明已发布”标记成审查完成。
		return
	}
	if !storageFailed {
		activity.Stage = "publish"
	}
	commentURL, err := s.postComment(item, result.Text)
	if err != nil {
		slog.Error("发布评论失败", "pr", item.SessionKey, "error", err)
		return
	}
	if !storageFailed {
		activity.State = "completed"
		activity.Stage = "published"
	}
	activity.CommentURL = commentURL
	s.progress(id, store.ProgressEvent{Kind: "publish", Summary: "已发布"})
	slog.Info("审查结果已发布", "pr", item.SessionKey, "session_saved", !storageFailed)
}

func (s *Server) runFix(ctx context.Context, item github.Review, a *store.Activity) {
	ws, err := review.PrepareWithBase(s.cfg.DataDir, item.Repo, item.CloneURL, item.HeadSHA, item.SessionKey, item.BaseBranch, item.BaseSHA)
	if err != nil {
		s.postFailure(item, "准备仓库失败", err)
		return
	}
	existing, _ := s.store.Get(item.SessionKey)
	a.Stage = "model"
	// Diff is intentionally empty: the model pulls tools instead of a pinned bulk patch (issue #21).
	result, err := s.runner.Fix(ctx, agent.ReviewInput{
		Review:    item,
		WorkDir:   ws.Dir,
		SessionID: existing.SessionID,
	})
	storageFailed := false
	if result.SessionID != "" {
		if saveErr := s.store.Put(item.SessionKey, store.Record{Repo: item.Repo, Number: item.Number, HeadSHA: item.HeadSHA, SessionID: result.SessionID}); saveErr != nil {
			storageFailed = true
			a.State, a.Stage = "failed", "storage"
			slog.Error("保存修复会话失败", "pr", item.SessionKey, "error", saveErr)
		}
	}
	if err != nil {
		slog.Error("修复失败", "pr", item.SessionKey, "error", err)
		s.postFailure(item, "修复失败", err)
		return
	}
	body := result.Text
	if strings.TrimSpace(body) == "" {
		body = "fix 会话已结束，但没有返回说明。"
	}
	if !storageFailed {
		a.Stage = "publish"
	}
	pub, pubErr := review.PublishFix(ws.Dir, item.HeadBranch, item.BaseBranch, publishMessage(item, result.Text), review.PublishLimits{
		MaxChangedFiles: s.cfg.Agent.FixMaxChangedFiles,
		MaxDiffBytes:    s.cfg.Agent.FixMaxDiffBytes,
		PushEnabled:     s.cfg.Agent.FixPushEnabled,
	})
	body = "fix 结果：\n\n" + body + "\n\n" + pub.Render()
	if pubErr != nil {
		slog.Error("发布修复提交失败", "pr", item.SessionKey, "error", pubErr)
		body += "\n\n提交/推送失败：" + publicFailure(pubErr)
		if commentURL, err := s.postComment(item, body); err != nil {
			slog.Error("发布修复评论失败", "pr", item.SessionKey, "error", err)
		} else {
			a.CommentURL = commentURL
		}
		return
	}
	if pub.Changed && pub.CommitSHA != "" {
		if saveErr := s.store.Put(item.SessionKey, store.Record{Repo: item.Repo, Number: item.Number, HeadSHA: pub.CommitSHA, SessionID: result.SessionID}); saveErr != nil {
			storageFailed = true
			a.State, a.Stage = "failed", "storage"
			slog.Error("保存修复提交记录失败", "pr", item.SessionKey, "error", saveErr)
		}
	}
	commentURL, err := s.postComment(item, body)
	if err != nil {
		slog.Error("发布修复评论失败", "pr", item.SessionKey, "error", err)
		return
	}
	if !storageFailed {
		a.State = "completed"
		a.Stage = "published"
	}
	a.CommentURL = commentURL
	slog.Info("修复完成", "pr", item.SessionKey, "changed", pub.Changed, "sha", pub.CommitSHA)
}

func publishMessage(item github.Review, _ string) string {
	return fmt.Sprintf("MojoReviewer fix: apply requested changes for PR #%d", item.Number)
}

func (s *Server) runRemember(item github.Review, a *store.Activity) {
	note := strings.TrimSpace(item.Instruction)
	if note == "" {
		if _, err := s.postComment(item, "没有可记住的内容。用法：`@"+strings.TrimPrefix(s.cfg.GitHub.TriggerUser, "@")+" remember <事实>`"); err != nil {
			slog.Error("发布 remember 用法失败", "pr", item.SessionKey, "error", err)
			return
		}
		a.State = "completed"
		a.Stage = "published"
		return
	}
	content := fmt.Sprintf("仓库 %s 的人工补充记忆：%s", item.Repo, note)
	if err := s.runner.RememberProject(content); err != nil {
		slog.Error("remember 失败", "pr", item.SessionKey, "error", err)
		s.postFailure(item, "写入项目记忆失败", err)
		return
	}
	if _, err := s.postComment(item, "已记住：\n"+note); err != nil {
		slog.Error("发布 remember 确认失败", "pr", item.SessionKey, "error", err)
		return
	}
	a.State = "completed"
	a.Stage = "published"
	slog.Info("remember 完成", "pr", item.SessionKey)
}

func fixAuthorAllowed(allow []string, author string) bool {
	if len(allow) == 0 {
		return true
	}
	author = strings.ToLower(strings.TrimSpace(author))
	author = strings.TrimPrefix(author, "@")
	for _, a := range allow {
		if a == author {
			return true
		}
	}
	return false
}

func (s *Server) runFixAuthorDenied(item github.Review, a *store.Activity) {
	msg := "当前账号无权触发 fix。允许的账号：" + strings.Join(s.cfg.Agent.FixAllowedAuthors, ", ")
	if _, err := s.postComment(item, msg); err != nil {
		slog.Error("发布 fix 权限拒绝评论失败", "pr", item.SessionKey, "error", err)
		return
	}
	a.State = "completed"
	a.Stage = "published"
	slog.Warn("fix 作者未授权", "pr", item.SessionKey, "author", item.Author)
}

func (s *Server) runFixUnsupported(item github.Review, a *store.Activity) {
	user := strings.TrimPrefix(s.cfg.GitHub.TriggerUser, "@")
	msg := "fix 模式未启用。请在配置里打开 `agent.fix_enabled`，或改用只读 review / remember。\n" +
		"可用：`@" + user + " review ...` / `@" + user + " remember ...`"
	if _, err := s.postComment(item, msg); err != nil {
		slog.Error("发布 fix 提示失败", "pr", item.SessionKey, "error", err)
		return
	}
	a.State = "completed"
	a.Stage = "published"
	slog.Info("fix 暂未开放，已回复", "pr", item.SessionKey)
}

func (s *Server) postComment(item github.Review, body string) (string, error) {
	return review.PostCommentURL(item.Repo, item.Number, body, s.cfg.GitHub.Host,
		s.cfg.Agent.APIKey, s.cfg.GitHub.Token, s.cfg.GitHub.WebhookSecret)
}

func (s *Server) postFailure(item github.Review, stage string, err error) {
	slog.Error(stage, "pr", item.SessionKey, "error", err)
	if _, postErr := s.postComment(item, stage+"："+publicFailure(err)); postErr != nil {
		slog.Error("发布失败评论失败", "pr", item.SessionKey, "error", postErr)
	}
}

// publicFailure 只给协作者一个归类后的原因。
// gh/git 的 stderr 可能带本地路径、内部主机或令牌片段，那些只留在日志里。
var (
	httpDenied  = regexp.MustCompile(`(?i)(?:\bhttp\b|\bstatus\b|\bcode\b)[\s:=_-]*(?:401|403)\b`)
	httpMissing = regexp.MustCompile(`(?i)(?:\bhttp\b|\bstatus\b|\bcode\b)[\s:=_-]*404\b`)
)

func publicFailure(err error) string {
	text := strings.ToLower(err.Error())
	switch {
	case strings.Contains(text, "authentication") || strings.Contains(text, "unauthorized") || strings.Contains(text, "forbidden") || strings.Contains(text, "bad credentials") || httpDenied.MatchString(text):
		return "访问 GitHub 被拒绝，请检查令牌权限"
	case strings.Contains(text, "timed out") || strings.Contains(text, "timeout") || strings.Contains(text, "deadline exceeded") || strings.Contains(text, "context canceled"):
		return "访问 GitHub 超时"
	case strings.Contains(text, "could not resolve") || strings.Contains(text, "no such host") || strings.Contains(text, "network") || strings.Contains(text, "connection refused") || strings.Contains(text, "connection reset"):
		return "无法连接 GitHub"
	case strings.Contains(text, "不一致"):
		return "PR 信息与请求的仓库或编号不一致"
	case strings.Contains(text, "缺少"):
		return "PR 信息缺少检出所需字段"
	case strings.Contains(text, "解析"):
		return "PR 信息无法解析"
	case strings.Contains(text, "not found") || strings.Contains(text, "does not exist") || strings.Contains(text, "repository not found") || httpMissing.MatchString(text):
		return "找不到 PR 或仓库"
	default:
		return "内部错误，详情见服务日志"
	}
}
