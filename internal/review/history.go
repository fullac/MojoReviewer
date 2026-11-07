// 保存跨提交发现的稳定身份，并生成有界续审背景和经过复核的状态变化。
package review

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
)

type Finding struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	Note        Note   `json:"note"`
	VerifiedSHA string `json:"verified_sha"`
}

type History struct {
	ReviewedSHA   string    `json:"reviewed_sha"`
	Findings      []Finding `json:"findings"`
	Omitted       int       `json:"omitted"`
	DeltaFiles    []string  `json:"delta_files"`
	DeltaComplete bool      `json:"delta_complete"`
}

func PreviousReview(ctx context.Context, dir, sha string, findings []Finding) *History {
	if sha == "" {
		return nil
	}
	h := &History{ReviewedSHA: sha, Findings: []Finding{}, DeltaFiles: []string{}}
	names, err := manifestGit(ctx, dir, 4<<20, "diff", "--name-only", "--no-renames", "-z", sha, "HEAD")
	if err == nil {
		h.DeltaComplete = true
		for _, p := range strings.Split(names, "\x00") {
			if p != "" {
				if len(h.DeltaFiles) >= 200 {
					h.DeltaComplete = false
					break
				}
				h.DeltaFiles = append(h.DeltaFiles, p)
			}
		}
	}
	for _, f := range findings {
		if f.Status == "resolved" {
			continue
		}
		h.Findings = append(h.Findings, f)
		raw, _ := json.Marshal(h)
		if len(raw) > 16<<10 {
			h.Findings = h.Findings[:len(h.Findings)-1]
			h.Omitted++
		}
	}
	// Extremely long path lists are also bounded; incompleteness is explicit.
	for {
		raw, _ := json.Marshal(h)
		if len(raw) <= 16<<10 || len(h.DeltaFiles) == 0 {
			break
		}
		h.DeltaFiles = h.DeltaFiles[:len(h.DeltaFiles)-1]
		h.DeltaComplete = false
	}
	return h
}

func FindingID(n Note) string {
	problem, _, _ := strings.Cut(n.Body, "\n\n- **代码位置：**")
	sum := sha256.Sum256([]byte(n.Path + "\x00" + strings.Join(strings.Fields(problem), " ")))
	return fmt.Sprintf("%x", sum[:12])
}

// Reconcile retains unmentioned findings as pending; absence never means fixed.
func Reconcile(previous []Finding, notes []Note, sha string) []Finding {
	out := []Finding{}
	indexes := map[string]int{}
	historical := map[string]bool{}
	observed := map[string]string{}
	for _, f := range previous {
		if f.Status == "resolved" {
			continue
		}
		f.Status = "pending"
		historical[f.ID] = true
		indexes[f.ID] = len(out)
		out = append(out, f)
	}
	for _, n := range notes {
		id := n.FindingID
		if id == "" {
			id = FindingID(n)
		}
		status := "new"
		if observed[id] == "persistent" && n.Status == "resolved" {
			continue
		}
		if i, ok := indexes[id]; ok {
			if historical[id] {
				status = "persistent"
			}
			if n.Status == "resolved" {
				status = "resolved"
			}
			observed[id] = status
			out[i] = Finding{ID: id, Status: status, Note: n, VerifiedSHA: sha}
		} else {
			// Unknown resolution IDs cannot resolve any historical finding.
			if n.Status == "resolved" {
				continue
			}
			indexes[id] = len(out)
			out = append(out, Finding{ID: id, Status: status, Note: n, VerifiedSHA: sha})
		}
	}
	return out
}

// RenderChanges keeps the existing severity groups and three-item body format.
func RenderChanges(findings []Finding) string {
	notes := &Notes{}
	pending := 0
	for _, f := range findings {
		if f.Status == "pending" {
			pending++
			continue
		}
		labels := map[string]string{"new": "新增", "persistent": "仍存在", "resolved": "已解决"}
		label := labels[f.Status]
		if label == "" {
			continue
		}
		n := f.Note
		n.Title = label + " · " + n.Title
		if f.Status == "resolved" {
			n.Severity = "建议"
		}
		_ = notes.AddWithTitle(n.Severity, n.Path, n.Line, n.Title, n.Body)
	}
	text := notes.Render()
	if pending > 0 {
		text += fmt.Sprintf("\n\n另有 %d 条历史发现本轮未完成复核，继续保留为待确认。", pending)
	}
	return text
}
