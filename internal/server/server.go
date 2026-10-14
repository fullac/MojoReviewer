package server

import (
	"context"
	"fmt"
	"io"
	"log"
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
	log.Printf("MojoReviewer 监听 %s", s.cfg.Server.Addr())
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
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"duplicate"}`))
		return
	}
	if existing, ok := s.store.Get(item.SessionKey); ok && item.Comment == "" && existing.HeadSHA == item.HeadSHA {
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

	s.pending[item.SessionKey+":"+item.HeadSHA] = true
	if err := s.store.Activity(store.Activity{ID: item.AttemptID, Repo: item.Repo, Number: item.Number, HeadSHA: item.HeadSHA, State: "queued"}); err != nil {
		log.Printf("记录排队状态失败")
	}
	s.jobs <- *item
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
	log.Printf("开始任务 %s intent=%s", item.SessionKey, item.Intent)

	key := item.SessionKey + ":" + item.HeadSHA
	s.mu.Lock()
	id := item.AttemptID
	s.mu.Unlock()
	if id == "" {
		id = fmt.Sprint(time.Now().UnixNano())
	}
	activity := store.Activity{ID: id, Repo: item.Repo, Number: item.Number, HeadSHA: item.HeadSHA, State: "running"}
	_ = s.store.Activity(activity)
	defer func() {
		if activity.State == "running" {
			activity.State = "failed"
		}
		if err := s.store.Activity(activity); err != nil {
			log.Printf("记录审查状态失败")
		}
		s.mu.Lock()
		delete(s.pending, key)
		s.mu.Unlock()
	}()
	// Once an automatic attempt actually starts, reserve its SHA even if setup fails.
	// Merely queued attempts remain recoverable after a process restart.
	if item.Comment == "" {
		existing, _ := s.store.Get(item.SessionKey)
		existing.Repo, existing.Number, existing.HeadSHA = item.Repo, item.Number, item.HeadSHA
		if err := s.store.Put(item.SessionKey, existing); err != nil {
			activity.Stage = "storage"
			return
		}
	}
	log.Printf("开始审查 %s", item.SessionKey)

	if item.Comment != "" {
		activity.Stage = "metadata"
		resolved, err := github.ResolveComment(ctx, item, s.cfg.GitHub.Host)
		if err != nil {
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
		s.postFailure(item, "准备仓库失败", err)
		return
	}
	diffText := s.diffText(item, ws.Dir)
	existing, _ := s.store.Get(item.SessionKey)
	activity.Stage = "model"
	result, err := s.runner.Review(ctx, agent.ReviewInput{
		Review:    item,
		WorkDir:   ws.Dir,
		Diff:      diffText,
		SessionID: existing.SessionID,
	})
	if result.SessionID != "" {
		_ = s.store.Put(item.SessionKey, store.Record{Repo: item.Repo, Number: item.Number, HeadSHA: item.HeadSHA, SessionID: result.SessionID})
	}
	if err != nil {
		log.Printf("审查失败 %s: %v", item.SessionKey, err)
		return
	}
	activity.Stage = "publish"
	commentURL, err := review.PostCommentURL(item.Repo, item.Number, result.Text, s.cfg.GitHub.Host)
	if err != nil {
		log.Printf("发布评论失败 %s: %v", item.SessionKey, err)
		return
	}
	activity.State = "completed"
	activity.Stage = "published"
	activity.CommentURL = commentURL
	log.Printf("审查完成 %s", item.SessionKey)
}

func (s *Server) runFix(ctx context.Context, item github.Review, a *store.Activity) {
	ws, err := review.PrepareWithBase(s.cfg.DataDir, item.Repo, item.CloneURL, item.HeadSHA, item.SessionKey, item.BaseBranch, item.BaseSHA)
	if err != nil {
		s.postFailure(item, "准备仓库失败", err)
		return
	}
	diffText := s.diffText(item, ws.Dir)
	existing, _ := s.store.Get(item.SessionKey)
	a.Stage = "model"
	result, err := s.runner.Fix(ctx, agent.ReviewInput{
		Review:    item,
		WorkDir:   ws.Dir,
		Diff:      diffText,
		SessionID: existing.SessionID,
	})
	if result.SessionID != "" {
		_ = s.store.Put(item.SessionKey, store.Record{Repo: item.Repo, Number: item.Number, HeadSHA: item.HeadSHA, SessionID: result.SessionID})
	}
	if err != nil {
		log.Printf("修复失败 %s: %v", item.SessionKey, err)
		s.postFailure(item, "修复失败", err)
		return
	}
	body := result.Text
	if strings.TrimSpace(body) == "" {
		body = "fix 会话已结束，但没有返回说明。"
	}
	a.Stage = "publish"
	pub, pubErr := review.PublishFix(ws.Dir, item.HeadBranch, item.BaseBranch, publishMessage(item, result.Text), review.PublishLimits{
		MaxChangedFiles: s.cfg.Agent.FixMaxChangedFiles,
		MaxDiffBytes:    s.cfg.Agent.FixMaxDiffBytes,
		PushEnabled:     s.cfg.Agent.FixPushEnabled,
	})
	body = "fix 结果：\n\n" + body + "\n\n" + pub.Render()
	if pubErr != nil {
		log.Printf("发布修复提交失败 %s: %v", item.SessionKey, pubErr)
		body += "\n\n提交/推送失败：" + publicFailure(pubErr)
		if err := review.PostComment(item.Repo, item.Number, body, s.cfg.GitHub.Host); err != nil {
			log.Printf("发布修复评论失败 %s: %v", item.SessionKey, err)
		}
		return
	}
	if pub.Changed && pub.CommitSHA != "" {
		_ = s.store.Put(item.SessionKey, store.Record{Repo: item.Repo, Number: item.Number, HeadSHA: pub.CommitSHA, SessionID: result.SessionID})
	}
	commentURL, err := review.PostCommentURL(item.Repo, item.Number, body, s.cfg.GitHub.Host)
	if err != nil {
		log.Printf("发布修复评论失败 %s: %v", item.SessionKey, err)
		return
	}
	a.State = "completed"
	a.Stage = "published"
	a.CommentURL = commentURL
	log.Printf("修复完成 %s changed=%v sha=%s", item.SessionKey, pub.Changed, pub.CommitSHA)
}

func publishMessage(item github.Review, agentText string) string {
	if note := strings.TrimSpace(item.Instruction); note != "" {
		return note
	}
	text := strings.TrimSpace(agentText)
	if text == "" {
		return "apply requested fix"
	}
	// 用首行当 commit message，避免整段审查笔记进标题
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}
	return text
}

func (s *Server) diffText(item github.Review, dir string) string {
	diffText := "（评论触发，沿用会话中的仓库上下文。请在仓库绝对路径内自行查看 diff。）"
	if item.BaseSHA != "" && item.HeadSHA != "" {
		text, err := review.Diff(dir, item.BaseSHA)
		if err != nil {
			log.Printf("读取 diff 失败 %s: %v", item.SessionKey, err)
			return "（diff 读取失败：" + err.Error() + "。请用 pr_diff_stat 和 pr_file_diff 查看。）"
		}
		return text
	}
	return diffText
}

func (s *Server) runRemember(item github.Review, a *store.Activity) {
	note := strings.TrimSpace(item.Instruction)
	if note == "" {
		_ = review.PostComment(item.Repo, item.Number, "没有可记住的内容。用法：`@"+strings.TrimPrefix(s.cfg.GitHub.TriggerUser, "@")+" remember <事实>`", s.cfg.GitHub.Host)
		a.State = "completed"
		a.Stage = "published"
		return
	}
	content := fmt.Sprintf("仓库 %s 的人工补充记忆：%s", item.Repo, note)
	if err := s.runner.RememberProject(content); err != nil {
		log.Printf("remember 失败 %s: %v", item.SessionKey, err)
		s.postFailure(item, "写入项目记忆失败", err)
		return
	}
	if _, err := review.PostCommentURL(item.Repo, item.Number, "已记住：\n"+note, s.cfg.GitHub.Host); err != nil {
		log.Printf("发布 remember 确认失败 %s: %v", item.SessionKey, err)
		return
	}
	a.State = "completed"
	a.Stage = "published"
	log.Printf("remember 完成 %s", item.SessionKey)
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
	if _, err := review.PostCommentURL(item.Repo, item.Number, msg, s.cfg.GitHub.Host); err != nil {
		log.Printf("发布 fix 权限拒绝评论失败 %s: %v", item.SessionKey, err)
		return
	}
	a.State = "completed"
	a.Stage = "published"
	log.Printf("fix 作者未授权 %s author=%s", item.SessionKey, item.Author)
}

func (s *Server) runFixUnsupported(item github.Review, a *store.Activity) {
	user := strings.TrimPrefix(s.cfg.GitHub.TriggerUser, "@")
	msg := "fix 模式未启用。请在配置里打开 `agent.fix_enabled`，或改用只读 review / remember。\n" +
		"可用：`@" + user + " review ...` / `@" + user + " remember ...`"
	if _, err := review.PostCommentURL(item.Repo, item.Number, msg, s.cfg.GitHub.Host); err != nil {
		log.Printf("发布 fix 提示失败 %s: %v", item.SessionKey, err)
		return
	}
	a.State = "completed"
	a.Stage = "published"
	log.Printf("fix 暂未开放，已回复 %s", item.SessionKey)
}

func (s *Server) postFailure(item github.Review, stage string, err error) {
	log.Printf("%s %s: %v", stage, item.SessionKey, err)
	if postErr := review.PostComment(item.Repo, item.Number, stage+"："+publicFailure(err), s.cfg.GitHub.Host); postErr != nil {
		log.Printf("发布失败评论失败 %s: %v", item.SessionKey, postErr)
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
