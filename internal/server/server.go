package server

import (
	"context"
	"fmt"
	"hash/fnv"
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

// Server 接收 GitHub webhook，用有限数量的 worker 审查不同 PR。
type Server struct {
	cfg      config.Config
	runner   reviewRunner
	store    *store.Store
	allowed  map[string]bool
	jobs     chan github.Review
	runners  []reviewRunner
	mu       sync.Mutex
	pending  map[string]bool
	prActive map[string]bool
	prWait   map[string][]github.Review
	// spawnWorker 为 worker 1..n-1 建运行时。默认是 base.CloneForWorker；
	// 测试注入它，才能真正覆盖「同一 PR 钉回原 worker」的分配逻辑。
	spawnWorker func(workerID int) (reviewRunner, error)
}

// New 创建服务。jobs 缓冲满时 webhook 返回 429，不丢进无界队列。
func New(cfg config.Config, runner *agent.Runner, st *store.Store) *Server {
	allowed := map[string]bool{}
	for _, repo := range cfg.GitHub.Repos {
		allowed[repo] = true
	}
	s := &Server{
		cfg: cfg, runner: runner, store: st, allowed: allowed,
		jobs: make(chan github.Review, 32), pending: map[string]bool{},
		prActive: map[string]bool{}, prWait: map[string][]github.Review{},
	}
	if runner != nil {
		s.spawnWorker = func(workerID int) (reviewRunner, error) {
			return runner.CloneForWorker(workerID)
		}
	}
	return s
}

// Start 启动 HTTP 服务和有限数量的审查 worker。不同 PR 可以并行，同一个 PR 串行。
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

	workers := s.cfg.Agent.ReviewConcurrency
	if workers < 1 {
		workers = 2
	}
	if err := s.initRunners(workers); err != nil {
		return err
	}
	for i := 0; i < workers; i++ {
		go s.worker(ctx)
	}
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
	status, payload, err := s.accept(item, r.Header.Get("X-GitHub-Delivery"))
	s.mu.Unlock()
	if err != nil {
		slog.Error("记录排队状态失败", "error", err)
		http.Error(w, "记录排队状态失败", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(status)
	_, _ = w.Write([]byte(payload))
}

// accept decides one webhook under s.mu. Duplicate checks, the queue cap, and
// enqueue stay together so concurrent deliveries cannot all pass the same SHA.
func (s *Server) accept(item *github.Review, delivery string) (int, string, error) {
	if item.Comment == "" && s.pending[item.SessionKey+":"+item.HeadSHA] {
		slog.Debug("跳过排队中的重复审查", "pr", item.SessionKey, "head", item.HeadSHA)
		return http.StatusOK, `{"status":"duplicate"}`, nil
	}
	if item.Comment == "" && s.store.HasAutoHead(item.SessionKey, item.HeadSHA) {
		slog.Debug("跳过已尝试的自动审查", "pr", item.SessionKey, "head", item.HeadSHA)
		return http.StatusOK, `{"status":"duplicate"}`, nil
	}
	item.AttemptID = fmt.Sprint(time.Now().UnixNano())
	if delivery != "" && len(delivery) <= 128 {
		item.AttemptID = "github:" + delivery
		seen, err := s.store.HasActivity(item.AttemptID)
		if err != nil {
			return 500, "", fmt.Errorf("读取投递记录失败")
		}
		if seen {
			return http.StatusOK, `{"status":"duplicate"}`, nil
		}
	}
	queued := len(s.jobs)
	for _, waiting := range s.prWait {
		queued += len(waiting)
	}
	if queued >= cap(s.jobs) {
		return http.StatusTooManyRequests, "队列已满\n", nil
	}
	if err := s.store.Activity(store.Activity{ID: item.AttemptID, Repo: item.Repo, Number: item.Number, HeadSHA: item.HeadSHA, State: "queued"}); err != nil {
		return http.StatusInternalServerError, "", err
	}
	if item.Comment == "" {
		s.pending[item.SessionKey+":"+item.HeadSHA] = true
	}
	s.enqueueLocked(*item)
	slog.Debug("任务已排队", "pr", item.SessionKey, "attempt", item.AttemptID)
	return http.StatusAccepted, `{"status":"queued"}`, nil
}

// enqueue keeps one PR on a single worker. A second delivery for that PR waits
// behind the active attempt instead of sharing its checkout and session.
func (s *Server) enqueue(item github.Review) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enqueueLocked(item)
}

// enqueueLocked requires s.mu. The jobs channel has room because the webhook
// counts both the channel and prWait against the same capacity before accepting.
func (s *Server) enqueueLocked(item github.Review) {
	if s.prActive[item.SessionKey] {
		s.prWait[item.SessionKey] = append(s.prWait[item.SessionKey], item)
		return
	}
	s.prActive[item.SessionKey] = true
	s.jobs <- item
}

func (s *Server) finishPR(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	waiting := s.prWait[key]
	if len(waiting) == 0 {
		delete(s.prActive, key)
		delete(s.prWait, key)
		return
	}
	next := waiting[0]
	s.prWait[key] = waiting[1:]
	s.jobs <- next
}

// worker 从队列取任务。任务结束后必须释放该 PR 的占用，否则后续投递永远排不进去。
func (s *Server) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case item := <-s.jobs:
			s.runQueued(ctx, item)
		}
	}
}

// runQueued 跑一个已入队的尝试，并保证无论正常返回还是 panic 都释放 PR 槽位。
// 没有这个 defer 时，一次 panic 就会让 prActive 永远为 true：该 PR 之后的投递
// 全部堆进 prWait，再也不会有 finishPR 把它们取走，而且这些残留会持续占用
// 32 的队列配额，最后新 PR 会被 429 拒收。
func (s *Server) runQueued(ctx context.Context, item github.Review) {
	defer s.finishPR(item.SessionKey)
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Error("审查任务内部错误", "pr", item.SessionKey, "panic", fmt.Sprint(recovered))
			// 回执只会在成功发布之后才被标成 completed，所以这里覆盖不到成功结果。
			_ = s.store.Activity(store.Activity{
				ID: item.AttemptID, Repo: item.Repo, Number: item.Number,
				HeadSHA: item.HeadSHA, State: "failed", Stage: "internal",
			})
		}
	}()
	s.runOneWithRunner(ctx, item, nil)
}

func (s *Server) runOne(ctx context.Context, item github.Review) {
	s.runOneWithRunner(ctx, item, s.runner)
}

func (s *Server) initRunners(workers int) error {
	if workers < 1 {
		workers = 1
	}
	if s.spawnWorker == nil {
		// Tests and embedders may provide a fake runner. It is already responsible
		// for its own synchronization, so keep it as a single logical runtime.
		s.runners = []reviewRunner{s.runner}
		return nil
	}
	runners := make([]reviewRunner, workers)
	runners[0] = s.runner
	for i := 1; i < workers; i++ {
		clone, err := s.spawnWorker(i)
		if err != nil {
			return fmt.Errorf("启动审查 worker %d: %w", i, err)
		}
		runners[i] = clone
	}
	s.runners = runners
	return nil
}

func (s *Server) runnerFor(sessionKey string) reviewRunner {
	if len(s.runners) == 0 {
		return s.runner
	}
	return s.runners[workerIDFor(sessionKey, len(s.runners))]
}

func workerIDFor(sessionKey string, workers int) int {
	if workers <= 1 {
		return 0
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(sessionKey))
	return int(h.Sum32() % uint32(workers))
}

// runnerForRecord 选出应该跑这个 PR 的 runtime。resume 为 false 表示没有可复用
// 的运行时：会话（包括分工会话）开在另一个 runtime 的 HomeDir 下，在本进程里
// 重新打开是不可能的，只能重建会话。缩容 worker 后就是这种情况。
func (s *Server) runnerForRecord(sessionKey string, rec store.Record, found bool) (reviewRunner, int, bool) {
	if len(s.runners) == 0 {
		return s.runner, 0, true
	}
	workerID := workerIDFor(sessionKey, len(s.runners))
	if !found {
		return s.runners[workerID], workerID, false
	}
	pinned, set := rec.Worker()
	if !set {
		// 旧记录没有固定 worker，它们的会话在基础 runtime（worker 0）下。
		pinned = 0
	}
	if pinned < 0 || pinned >= len(s.runners) {
		return s.runners[workerID], workerID, false
	}
	return s.runners[pinned], pinned, true
}

func (s *Server) runOneWithRunner(ctx context.Context, item github.Review, selected reviewRunner) {
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
	existing, found := s.store.Get(item.SessionKey)
	selected, workerID, resume := s.runnerForRecord(item.SessionKey, existing, found)
	if !resume {
		// 会话和分工会话都开在那个 worker 的 HomeDir 下；换了 worker 就是打不开，
		// 这里直接重建，不要拿旧会话去碰运气。
		existing.SessionID = ""
		existing.RoleSessions = nil
	}
	// Once an automatic attempt actually starts, reserve its SHA even if setup fails.
	// Merely queued attempts remain recoverable after a process restart.
	if item.Comment == "" {
		reserved, err := s.store.ReserveAutoHead(item.SessionKey, store.Record{
			Repo: item.Repo, Number: item.Number, HeadSHA: item.HeadSHA,
			SessionID: existing.SessionID, RoleSessions: existing.RoleSessions,
			WorkerID: store.PinWorker(workerID),
		})
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
		s.runRememberWithRunner(item, &activity, selected)
		return
	case github.IntentFix:
		if !selected.FixEnabled() {
			s.runFixUnsupported(item, &activity)
			return
		}
		if !fixAuthorAllowed(s.cfg.Agent.FixAllowedAuthors, item.Author) {
			s.runFixAuthorDenied(item, &activity)
			return
		}
		s.runFixWithRunner(ctx, item, &activity, selected, workerID, existing)
		return
	}
	ws, err := review.PrepareWithBase(s.cfg.DataDir, item.Repo, item.CloneURL, item.HeadSHA, item.SessionKey, item.BaseBranch, item.BaseSHA)
	if err != nil {
		s.postFailure(item, "准备仓库失败", err)
		return
	}
	activity.Stage = "model"
	// Diff is intentionally empty: the model pulls pr_diff_stat / pr_file_diff (issue #21).
	result, err := selected.Review(ctx, agent.ReviewInput{
		Review:       item,
		WorkDir:      ws.Dir,
		SessionID:    existing.SessionID,
		RoleSessions: existing.RoleSessions,
	})
	storageFailed := false
	if result.SessionID != "" {
		if saveErr := s.store.Put(item.SessionKey, store.Record{
			Repo: item.Repo, Number: item.Number, HeadSHA: item.HeadSHA,
			SessionID: result.SessionID, RoleSessions: result.RoleSessions,
			WorkerID: store.PinWorker(workerID),
		}); saveErr != nil {
			storageFailed = true
			activity.State, activity.Stage = "failed", "storage"
			slog.Error("保存审查会话失败", "pr", item.SessionKey, "error", saveErr)
		}
	}
	if err != nil {
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
	slog.Info("审查结果已发布", "pr", item.SessionKey, "session_saved", !storageFailed)
}

func (s *Server) runFix(ctx context.Context, item github.Review, a *store.Activity) {
	existing, _ := s.store.Get(item.SessionKey)
	s.runFixWithRunner(ctx, item, a, s.runner, 0, existing)
}

func (s *Server) runFixWithRunner(ctx context.Context, item github.Review, a *store.Activity, selected reviewRunner, workerID int, existing store.Record) {
	ws, err := review.PrepareWithBase(s.cfg.DataDir, item.Repo, item.CloneURL, item.HeadSHA, item.SessionKey, item.BaseBranch, item.BaseSHA)
	if err != nil {
		s.postFailure(item, "准备仓库失败", err)
		return
	}
	a.Stage = "model"
	// Diff is intentionally empty: the model pulls tools instead of a pinned bulk patch (issue #21).
	result, err := selected.Fix(ctx, agent.ReviewInput{
		Review:    item,
		WorkDir:   ws.Dir,
		SessionID: existing.SessionID,
	})
	storageFailed := false
	if result.SessionID != "" {
		if saveErr := s.store.Put(item.SessionKey, store.Record{
			Repo: item.Repo, Number: item.Number, HeadSHA: item.HeadSHA,
			SessionID: result.SessionID, RoleSessions: existing.RoleSessions,
			WorkerID: store.PinWorker(workerID),
		}); saveErr != nil {
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
		if saveErr := s.store.Put(item.SessionKey, store.Record{
			Repo: item.Repo, Number: item.Number, HeadSHA: pub.CommitSHA,
			SessionID: result.SessionID, RoleSessions: existing.RoleSessions,
			WorkerID: store.PinWorker(workerID),
		}); saveErr != nil {
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
	s.runRememberWithRunner(item, a, s.runner)
}

func (s *Server) runRememberWithRunner(item github.Review, a *store.Activity, selected reviewRunner) {
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
	if err := selected.RememberProject(content); err != nil {
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
