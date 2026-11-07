// 发布前确定性核验笔记的位置与现有代码标识符，过滤无法定位的结论。
package review

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ReadRange 只记录工具实际返回的完整源码行，不保存源码或请求参数。
type ReadRange struct {
	Path       string
	Start, End int
	Old        bool
}

var inlineCode = regexp.MustCompile("`([^`\\n]+)`")
var identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)*(?:\(\))?$`)

// Ground 校验正常和中断审查的笔记。hunk 指 Git 补丁中的改动行段。
// 删除文件的行号以 merge-base 为准；其他文件以 head 为准。
func Ground(ctx context.Context, dir string, notes []Note, reads []ReadRange, previous ...Finding) (*Notes, int, error) {
	out := &Notes{}
	if len(notes) == 0 {
		return out, 0, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	names, err := manifestGit(ctx, dir, 4<<20, "diff", "--name-only", "--no-renames", "-z", mergeBaseRef, "HEAD")
	if err != nil {
		return out, len(notes), fmt.Errorf("审查依据校验失败")
	}
	changed := map[string]bool{}
	for _, name := range strings.Split(names, "\x00") {
		if name != "" {
			changed[name] = true
		}
	}
	rejected := 0
	scope := ToolScope{Dir: dir}
	evidence := &groundEvidence{}
	corpus := evidence.readCorpus(ctx, scope, reads)
	for _, n := range notes {
		if !groundNote(ctx, scope, changed, n, reads, evidence, corpus, previous) {
			rejected++
			continue
		}
		if err := out.AddFinding(n); err != nil {
			rejected++
		}
	}
	return out, rejected, nil
}

const (
	groundReadSideLimit  = 32
	groundReadByteLimit  = 1 << 20
	groundBlobCacheLimit = 8 << 20
)

// groundEvidence 在一次 Ground 内复用已取出的文件。
// 定位文件保留全文；已读范围按文件侧合并后只保留对应行，不能放宽成整文件。
type groundEvidence struct {
	blobs   map[string]string
	blobErr map[string]error
	bytes   int
}

func (e *groundEvidence) blob(ctx context.Context, scope ToolScope, ref, path string) (string, error) {
	key := ref + "\x00" + path
	if e.blobs != nil {
		if err, ok := e.blobErr[key]; ok {
			return "", err
		}
		if text, ok := e.blobs[key]; ok {
			return text, nil
		}
	}
	text, err := manifestGit(ctx, scope.Dir, 4<<20, "show", ref+":"+path)
	if e.blobs == nil {
		e.blobs = map[string]string{}
		e.blobErr = map[string]error{}
	}
	if err != nil {
		e.blobErr[key] = err
		return "", err
	}
	// 缓存满后仍允许原有定位文件检查，不能让额外读取挤掉合法笔记。
	if e.bytes+len(text) <= groundBlobCacheLimit {
		e.blobs[key] = text
		e.bytes += len(text)
	}
	return text, nil
}

// mergeReadRanges 把同一文件同一侧重叠或相邻的行段收成一段。
// 不相邻的行段保持分开，避免中间未读行进入依据。
func mergeReadRanges(reads []ReadRange) []ReadRange {
	type side struct {
		path string
		old  bool
	}
	grouped := map[side][]ReadRange{}
	order := make([]side, 0)
	for _, r := range reads {
		if r.Start < 1 || r.End < r.Start || r.Path == "" {
			continue
		}
		key := side{r.Path, r.Old}
		if _, ok := grouped[key]; !ok {
			order = append(order, key)
		}
		grouped[key] = append(grouped[key], r)
	}
	out := make([]ReadRange, 0, len(reads))
	for _, key := range order {
		spans := grouped[key]
		sort.Slice(spans, func(i, j int) bool { return spans[i].Start < spans[j].Start })
		cur := spans[0]
		for _, next := range spans[1:] {
			if next.Start <= cur.End+1 {
				if next.End > cur.End {
					cur.End = next.End
				}
				continue
			}
			out = append(out, cur)
			cur = next
		}
		out = append(out, cur)
	}
	return out
}

// readCorpus 只收集安全路径上实际读过的行。
// 32 的上限按不同文件侧计算；同一侧的多条 patch 行只占用一次。
func (e *groundEvidence) readCorpus(ctx context.Context, scope ToolScope, reads []ReadRange) string {
	var b strings.Builder
	sides := 0
	seen := map[string]bool{}
	for _, r := range mergeReadRanges(reads) {
		side := "new\x00" + r.Path
		if r.Old {
			side = "old\x00" + r.Path
		}
		if !seen[side] {
			if sides >= groundReadSideLimit {
				continue
			}
			seen[side] = true
			sides++
		}
		rel, err := scope.clean(r.Path)
		if err != nil || filepath.ToSlash(rel) != r.Path {
			continue
		}
		info, err := os.Lstat(filepath.Join(scope.Dir, rel))
		if r.Old {
			if err != nil && !os.IsNotExist(err) {
				continue
			}
			if err == nil && !info.Mode().IsRegular() {
				continue
			}
		} else if err != nil || !info.Mode().IsRegular() {
			continue
		}
		ref := "HEAD"
		if r.Old {
			ref = mergeBaseRef
		}
		text, err := e.blob(ctx, scope, ref, r.Path)
		if err != nil {
			continue
		}
		lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
		if r.Start > len(lines) {
			continue
		}
		end := r.End
		if end > len(lines) {
			end = len(lines)
		}
		// 体积预算针对实际已读片段，不针对可能很大的未读文件正文。
		size := 0
		for i := r.Start; i <= end; i++ {
			size += len(lines[i-1]) + 1
			if size > groundReadByteLimit-b.Len() {
				break
			}
		}
		if size > groundReadByteLimit-b.Len() {
			continue
		}
		for i := r.Start; i <= end; i++ {
			b.WriteString(lines[i-1])
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func groundNote(ctx context.Context, scope ToolScope, changed map[string]bool, n Note, reads []ReadRange, evidence *groundEvidence, corpus string, previous []Finding) bool {
	known := false
	previousSHA := ""
	for _, f := range previous {
		if n.FindingID == f.ID && f.Status != "resolved" && n.Path == f.Note.Path {
			known = true
			previousSHA = f.VerifiedSHA
		}
	}
	if n.FindingID != "" && !known {
		return false
	}
	if n.Status == "resolved" && !known {
		return false
	}
	if n.Line < 1 || (!changed[n.Path] && !known) || filepath.IsAbs(n.Path) {
		return false
	}
	rel, err := scope.clean(n.Path)
	if err != nil || filepath.ToSlash(rel) != n.Path {
		return false
	}
	// References in the location item must match the structured position exactly.
	sections := strings.Split(n.Body, "- **代码位置：** ")
	if len(sections) != 2 {
		return false
	}
	location, _, ok := strings.Cut(sections[1], "\n\n- **修复建议：** ")
	if !ok || strings.TrimSpace(location) != fmt.Sprintf("`%s:%d`", n.Path, n.Line) {
		return false
	}
	ref := "HEAD"
	deleted := false
	if info, err := os.Lstat(filepath.Join(scope.Dir, rel)); os.IsNotExist(err) {
		ref = mergeBaseRef
		deleted = true
	} else if err != nil || !info.Mode().IsRegular() {
		return false
	}
	source, err := evidence.blob(ctx, scope, ref, n.Path)
	if err != nil || n.Line > len(strings.Split(strings.TrimSuffix(source, "\n"), "\n")) {
		return false
	}
	patch, err := manifestGit(ctx, scope.Dir, 4<<20, "diff", "--no-ext-diff", "--no-textconv", "--no-renames", "-U0", mergeBaseRef, "HEAD", "--", ":(literal)"+n.Path)
	if err != nil {
		return false
	}
	if deleted {
		// Reverse the diff so parseChangedLines reads the old-side coordinates.
		patch, err = manifestGit(ctx, scope.Dir, 4<<20, "diff", "--no-ext-diff", "--no-textconv", "--no-renames", "-U0", "HEAD", mergeBaseRef, "--", ":(literal)"+n.Path)
		if err != nil {
			return false
		}
	}
	located := false
	for _, span := range parseChangedLines(patch) {
		if n.Line >= span[0] && n.Line <= span[1] {
			located = true
		}
	}
	read := false
	for _, r := range reads {
		if r.Path == n.Path && r.Old == deleted && n.Line >= r.Start && n.Line <= r.End {
			read = true
		}
	}
	located = located || read
	if known && !read {
		return false
	}
	if !located {
		return false
	}
	if n.Status == "resolved" && commitID.MatchString(previousSHA) {
		// A resolved finding may name an identifier that was removed by the fix.
		if old, err := evidence.blob(ctx, scope, previousSHA, n.Path); err == nil {
			source += "\n" + old
		}
	}
	// 定位文件用全文，其他文件只用调用前一次拼好的已读行。
	noteCorpus := source + "\n" + corpus
	// Only claims about existing code are checked. Proposed names in the fix
	// item are allowed to be new. This is a syntactic check, not a semantic judge.
	for _, m := range inlineCode.FindAllStringSubmatch(sections[0], -1) {
		term := strings.TrimSuffix(m[1], "()")
		if identifier.MatchString(m[1]) {
			re := regexp.MustCompile(`\b` + regexp.QuoteMeta(term) + `\b`)
			if !re.MatchString(noteCorpus) {
				return false
			}
		}
	}
	return true
}

// ReturnedRanges ignores headers, clipped partial lines and beyond-EOF messages.
func ReturnedRanges(path, output string) []ReadRange {
	var out []ReadRange
	lines := strings.Split(output, "\n")
	for i, line := range lines {
		if i == 0 || i == len(lines)-1 {
			continue
		}
		before, _, ok := strings.Cut(line, "\t")
		n, err := strconv.Atoi(before)
		if !ok || err != nil || n < 1 {
			continue
		}
		// clip() appends a blank line and marker after a possibly partial line.
		if i+2 < len(lines) && lines[i+1] == "" && strings.HasPrefix(lines[i+2], "…输出过长") {
			continue
		}
		if len(out) > 0 && out[len(out)-1].End+1 == n {
			out[len(out)-1].End = n
		} else {
			out = append(out, ReadRange{Path: path, Start: n, End: n})
		}
	}
	return out
}

// ReturnedPatchRanges tracks both sides of the patch actually sent to the model.
func ReturnedPatchRanges(path, output string) []ReadRange {
	var out []ReadRange
	header := regexp.MustCompile(`^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@`)
	old, current := 0, 0
	active := false
	lines := strings.Split(output, "\n")
	add := func(n int, isOld bool) {
		if n > 0 {
			out = append(out, ReadRange{Path: path, Start: n, End: n, Old: isOld})
		}
	}
	for i, line := range lines {
		if m := header.FindStringSubmatch(line); m != nil {
			old, _ = strconv.Atoi(m[1])
			current, _ = strconv.Atoi(m[2])
			active = true
			continue
		}
		if !active || line == "" {
			continue
		}
		if i+2 < len(lines) && lines[i+1] == "" && strings.HasPrefix(lines[i+2], "…输出过长") {
			break
		}
		switch line[0] {
		case ' ':
			add(old, true)
			add(current, false)
			old++
			current++
		case '-':
			add(old, true)
			old++
		case '+':
			add(current, false)
			current++
		case '\\':
		default:
			active = false
		}
	}
	return out
}
