// Package observe 收集一次审查的阶段耗时和脱敏模型用量，供持久化、管理台及回放评测共用。
package observe

import (
	"context"
	"sync"
	"time"
)

const MaxTools = 400

type Usage struct {
	Input  int `json:"input_tokens"`
	Output int `json:"output_tokens"`
	Total  int `json:"total_tokens"`
	Cached int `json:"cached_input_tokens"`
}

type Tool struct {
	Name        string `json:"name"`
	DurationMS  int64  `json:"duration_ms"`
	ResultBytes int    `json:"result_bytes"`
}

type Snapshot struct {
	MemoryCacheHit *bool            `json:"memory_cache_hit,omitempty"`
	MemoryFiles    int              `json:"memory_files"`
	StartedAt      string           `json:"started_at"`
	TotalMS        int64            `json:"total_ms"`
	Stages         map[string]int64 `json:"stages_ms"`
	Iterations     int              `json:"iterations"`
	Usage          *Usage           `json:"usage"`
	ToolCount      int              `json:"tool_count"`
	Tools          []Tool           `json:"tools"`
}

type Recorder struct {
	mu      sync.Mutex
	started time.Time
	data    Snapshot
}

type contextKey struct{}

func Start(ctx context.Context) (context.Context, *Recorder) {
	r := &Recorder{started: time.Now(), data: Snapshot{Stages: map[string]int64{}, Tools: []Tool{}}}
	r.data.StartedAt = r.started.UTC().Format(time.RFC3339Nano)
	return context.WithValue(ctx, contextKey{}, r), r
}

func From(ctx context.Context) *Recorder {
	r, _ := ctx.Value(contextKey{}).(*Recorder)
	return r
}

// Stage 只接受宿主定义的阶段名；结束函数应在成功和失败路径都调用。
func Stage(ctx context.Context, name string) func() {
	r := From(ctx)
	if r == nil {
		return func() {}
	}
	start := time.Now()
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.data.Stages[name] += time.Since(start).Milliseconds()
		})
	}
}

// Model 只接收数值用量，不接收模型回复、工具参数或原始错误。
func (r *Recorder) Model(iterations int, usage *Usage) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data.Iterations += iterations
	if usage != nil {
		if r.data.Usage == nil {
			r.data.Usage = &Usage{}
		}
		r.data.Usage.Input += usage.Input
		r.data.Usage.Output += usage.Output
		r.data.Usage.Total += usage.Total
		r.data.Usage.Cached += usage.Cached
	}
}

func (r *Recorder) Tool(name string, duration time.Duration, resultBytes int) {
	if r == nil {
		return
	}
	switch name {
	case "pr_diff_stat", "pr_file_diff", "read_repo_file", "repo_grep", "changed_symbols", "tests_around", "review_note",
		"file_read", "file_list", "file_write", "file_patch", "file_mkdir":
	default:
		name = "unknown"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data.ToolCount++
	if len(r.data.Tools) < MaxTools {
		r.data.Tools = append(r.data.Tools, Tool{Name: name, DurationMS: duration.Milliseconds(), ResultBytes: resultBytes})
	}
}

func (r *Recorder) Snapshot() *Snapshot {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.data
	if r.data.MemoryCacheHit != nil {
		hit := *r.data.MemoryCacheHit
		out.MemoryCacheHit = &hit
	}
	out.TotalMS = time.Since(r.started).Milliseconds()
	out.Stages = make(map[string]int64, len(r.data.Stages))
	for k, v := range r.data.Stages {
		out.Stages[k] = v
	}
	out.Tools = append([]Tool{}, r.data.Tools...)
	if r.data.Usage != nil {
		u := *r.data.Usage
		out.Usage = &u
	}
	return &out
}

func (r *Recorder) Memory(hit bool, files int) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data.MemoryCacheHit = &hit
	r.data.MemoryFiles = files
}
