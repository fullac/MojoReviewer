package server

import (
	"context"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/mojoreviewer/mojoreviewer/internal/agent"
	"github.com/mojoreviewer/mojoreviewer/internal/config"
	"github.com/mojoreviewer/mojoreviewer/internal/github"
	"github.com/mojoreviewer/mojoreviewer/internal/review"
	"github.com/mojoreviewer/mojoreviewer/internal/store"
)

type reviewRunner interface {
	Review(context.Context, agent.ReviewInput) (agent.ReviewResult, error)
}

// Server 接收 GitHub webhook，串行跑只读审查。
type Server struct {
	cfg     config.Config
	runner  reviewRunner
	store   *store.Store
	allowed map[string]bool
	jobs    chan github.Review
}

// New 创建服务。jobs 缓冲满时 webhook 返回 429，不丢进无界队列。
func New(cfg config.Config, runner *agent.Runner, st *store.Store) *Server {
	allowed := map[string]bool{}
	for _, repo := range cfg.GitHub.Repos {
		allowed[repo] = true
	}
	return &Server{cfg: cfg, runner: runner, store: st, allowed: allowed, jobs: make(chan github.Review, 32)}
}

// Start 启动 HTTP 服务和单个审查 worker。
func (s *Server) Start(ctx context.Context) error {
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
	if existing, ok := s.store.Get(item.SessionKey); ok && item.Comment == "" && existing.HeadSHA == item.HeadSHA {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"duplicate"}`))
		return
	}
	select {
	case s.jobs <- *item:
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"status":"queued"}`))
	default:
		http.Error(w, "队列已满", http.StatusTooManyRequests)
	}
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
	log.Printf("开始审查 %s", item.SessionKey)
	if item.Comment != "" {
		resolved, err := github.ResolveComment(ctx, item, s.cfg.GitHub.Host)
		if err != nil {
			s.postFailure(item, "补齐 PR 信息失败", err)
			return
		}
		item = resolved
	}
	ws, err := review.PrepareWithBase(s.cfg.DataDir, item.Repo, item.CloneURL, item.HeadSHA, item.SessionKey, item.BaseBranch, item.BaseSHA)
	if err != nil {
		s.postFailure(item, "准备仓库失败", err)
		return
	}
	diffText := "（评论触发，沿用会话中的仓库上下文。请在仓库绝对路径内自行查看 diff。）"
	if item.BaseSHA != "" && item.HeadSHA != "" {
		diffText, err = review.Diff(ws.Dir, item.BaseSHA)
		if err != nil {
			log.Printf("读取 diff 失败 %s: %v", item.SessionKey, err)
			diffText = "（diff 读取失败：" + err.Error() + "。请用 pr_diff_stat 和 pr_file_diff 查看。）"
		}
	}
	existing, _ := s.store.Get(item.SessionKey)
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
	if err := review.PostComment(item.Repo, item.Number, result.Text, s.cfg.GitHub.Host); err != nil {
		log.Printf("发布评论失败 %s: %v", item.SessionKey, err)
		return
	}
	log.Printf("审查完成 %s", item.SessionKey)
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
