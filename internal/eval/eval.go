// Package eval 在本地裸仓库的固定提交上回放审查，以标注位置衡量结果并输出脱敏观测。
package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/mojoreviewer/mojoreviewer/internal/agent"
	"github.com/mojoreviewer/mojoreviewer/internal/github"
	"github.com/mojoreviewer/mojoreviewer/internal/observe"
	"github.com/mojoreviewer/mojoreviewer/internal/review"
)

type Expected struct {
	Path  string `json:"path"`
	Start int    `json:"start_line"`
	End   int    `json:"end_line"`
}

type Case struct {
	ID         string     `json:"id"`
	Repository string     `json:"repository"`
	BaseSHA    string     `json:"base_sha"`
	HeadSHA    string     `json:"head_sha"`
	Title      string     `json:"title"`
	Expected   []Expected `json:"expected"`
}

type Scores struct {
	TP                int     `json:"true_positive"`
	FP                int     `json:"false_positive"`
	FN                int     `json:"false_negative"`
	Precision         float64 `json:"precision"`
	Recall            float64 `json:"recall"`
	F1                float64 `json:"f1"`
	HallucinationRate float64 `json:"hallucination_rate"`
}

type Result struct {
	ID      string            `json:"id"`
	BaseSHA string            `json:"base_sha"`
	HeadSHA string            `json:"head_sha"`
	State   string            `json:"state"`
	Reason  string            `json:"reason,omitempty"`
	Scores  *Scores           `json:"scores,omitempty"`
	Metrics *observe.Snapshot `json:"metrics"`
}

type Report struct {
	SchemaVersion int      `json:"schema_version"`
	Matching      string   `json:"matching"`
	Cases         []Result `json:"cases"`
	Completed     int      `json:"completed"`
	Failed        int      `json:"failed"`
	Scores        Scores   `json:"scores"`
}

type Runner interface {
	Review(context.Context, agent.ReviewInput) (agent.ReviewResult, error)
}

var shaPattern = regexp.MustCompile(`^(?:[a-fA-F0-9]{40}|[a-fA-F0-9]{64})$`)
var idPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,80}$`)

// LoadCases 拒绝网络仓库、浮动引用和缺少标注的样本。expected: [] 明确代表无问题样本。
func LoadCases(path string) ([]Case, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cases []Case
	if err := json.Unmarshal(raw, &cases); err != nil {
		return nil, err
	}
	if len(cases) == 0 {
		return nil, fmt.Errorf("评测样本为空")
	}
	seen := map[string]bool{}
	for i := range cases {
		c := &cases[i]
		if !idPattern.MatchString(c.ID) || seen[c.ID] {
			return nil, fmt.Errorf("样本 ID 无效或重复")
		}
		seen[c.ID] = true
		if !shaPattern.MatchString(c.BaseSHA) || !shaPattern.MatchString(c.HeadSHA) {
			return nil, fmt.Errorf("样本 %s 需要完整提交 SHA", c.ID)
		}
		if c.Expected == nil {
			return nil, fmt.Errorf("样本 %s 缺少 expected 标注", c.ID)
		}
		for _, e := range c.Expected {
			if e.Path == "" || filepath.IsAbs(e.Path) || filepath.Clean(e.Path) != e.Path || e.Path == ".." || strings.HasPrefix(e.Path, "../") || e.Start < 1 || e.End < e.Start {
				return nil, fmt.Errorf("样本 %s 标注无效", c.ID)
			}
		}
		if c.Repository == "" || strings.Contains(c.Repository, "://") {
			return nil, fmt.Errorf("样本 %s 只接受本地裸仓库", c.ID)
		}
		if !filepath.IsAbs(c.Repository) {
			c.Repository = filepath.Join(filepath.Dir(path), c.Repository)
		}
		c.Repository, err = filepath.Abs(c.Repository)
		if err != nil {
			return nil, err
		}
		if st, err := os.Stat(c.Repository); err != nil || !st.IsDir() {
			return nil, fmt.Errorf("样本 %s 本地仓库不可用", c.ID)
		}
		bare, err := localGit(c.Repository, "rev-parse", "--is-bare-repository")
		if err != nil || bare != "true" {
			return nil, fmt.Errorf("样本 %s 需要裸仓库", c.ID)
		}
		// A promisor clone can lazily contact a remote while preparing the checkout.
		if promisor, _ := localGit(c.Repository, "config", "--get-regexp", `remote\..*\.promisor`); promisor != "" {
			return nil, fmt.Errorf("样本 %s 不接受部分克隆仓库", c.ID)
		}
		for _, sha := range []string{c.BaseSHA, c.HeadSHA} {
			if _, err := localGit(c.Repository, "cat-file", "-e", sha+"^{commit}"); err != nil {
				return nil, fmt.Errorf("样本 %s 提交不在本地仓库", c.ID)
			}
		}
	}
	return cases, nil
}

func localGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

func Run(ctx context.Context, cases []Case, dataDir string, runner Runner) Report {
	report := Report{SchemaVersion: 1, Matching: "path + inclusive line range; one-to-one maximum matching; hallucination_rate = unmatched predictions / predictions (location proxy, not semantic verification)", Cases: []Result{}}
	for _, c := range cases {
		result := runCase(ctx, c, dataDir, runner)
		report.Cases = append(report.Cases, result)
		if result.Scores != nil {
			report.Completed++
			report.Scores.TP += result.Scores.TP
			report.Scores.FP += result.Scores.FP
			report.Scores.FN += result.Scores.FN
		} else {
			report.Failed++
		}
	}
	report.Scores.calculate()
	return report
}

func runCase(ctx context.Context, c Case, dataDir string, runner Runner) Result {
	ctx, metrics := observe.Start(ctx)
	result := Result{ID: c.ID, BaseSHA: c.BaseSHA, HeadSHA: c.HeadSHA, State: "failed"}
	done := observe.Stage(ctx, "checkout")
	ws, err := review.PrepareWithBase(dataDir, "eval/local", c.Repository, c.HeadSHA, c.ID, "", c.BaseSHA)
	done()
	if err != nil {
		result.Reason = "checkout"
		result.Metrics = metrics.Snapshot()
		return result
	}
	reviewed, err := runner.Review(ctx, agent.ReviewInput{Review: github.Review{Repo: "eval/local", Number: 1, SessionKey: "eval/" + c.ID, Title: c.Title, BaseSHA: c.BaseSHA, HeadSHA: c.HeadSHA}, WorkDir: ws.Dir})
	result.Metrics = metrics.Snapshot()
	if err != nil {
		result.Reason = "model"
		return result
	}
	scores := Match(reviewed.Notes, c.Expected)
	result.Scores = &scores
	result.State = "completed"
	return result
}

// Match 使用最大一对一匹配；重复发现不能重复领取同一个标注的命中数。
func Match(notes []review.Note, expected []Expected) Scores {
	assigned := make([]int, len(expected))
	for i := range assigned {
		assigned[i] = -1
	}
	var visit func(int, []bool) bool
	visit = func(n int, seen []bool) bool {
		for i, e := range expected {
			if seen[i] || notes[n].Path != e.Path || notes[n].Line < e.Start || notes[n].Line > e.End {
				continue
			}
			seen[i] = true
			if assigned[i] < 0 || visit(assigned[i], seen) {
				assigned[i] = n
				return true
			}
		}
		return false
	}
	tp := 0
	for n := range notes {
		if visit(n, make([]bool, len(expected))) {
			tp++
		}
	}
	scores := Scores{TP: tp, FP: len(notes) - tp, FN: len(expected) - tp}
	scores.calculate()
	return scores
}

func (s *Scores) calculate() {
	// Empty denominators use zero; raw counts make clean-only runs unambiguous.
	if n := s.TP + s.FP; n > 0 {
		s.Precision = float64(s.TP) / float64(n)
		s.HallucinationRate = float64(s.FP) / float64(n)
	}
	if n := s.TP + s.FN; n > 0 {
		s.Recall = float64(s.TP) / float64(n)
	}
	if s.Precision+s.Recall > 0 {
		s.F1 = 2 * s.Precision * s.Recall / (s.Precision + s.Recall)
	}
}
