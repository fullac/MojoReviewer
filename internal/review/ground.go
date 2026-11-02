// 发布前确定性核验笔记的位置与现有代码标识符，过滤无法定位的结论。
package review

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
	for _, n := range notes {
		if !groundNote(ctx, ToolScope{Dir: dir}, changed, n, reads, previous) {
			rejected++
			continue
		}
		if err := out.AddFinding(n); err != nil {
			rejected++
		}
	}
	return out, rejected, nil
}

func groundNote(ctx context.Context, scope ToolScope, changed map[string]bool, n Note, reads []ReadRange, previous []Finding) bool {
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
	source, err := manifestGit(ctx, scope.Dir, 4<<20, "show", ref+":"+n.Path)
	if err != nil || n.Line > len(strings.Split(strings.TrimSuffix(source, "\n"), "\n")) {
		return false
	}
	patch, err := manifestGit(ctx, scope.Dir, 4<<20, "diff", "--no-ext-diff", "--no-textconv", "--no-renames", "-U0", mergeBaseRef, "HEAD", "--", n.Path)
	if err != nil {
		return false
	}
	if deleted {
		// Reverse the diff so parseChangedLines reads the old-side coordinates.
		patch, err = manifestGit(ctx, scope.Dir, 4<<20, "diff", "--no-ext-diff", "--no-textconv", "--no-renames", "-U0", "HEAD", mergeBaseRef, "--", n.Path)
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
		if old, err := manifestGit(ctx, scope.Dir, 4<<20, "show", previousSHA+":"+n.Path); err == nil {
			source += "\n" + old
		}
	}
	// Only claims about existing code are checked. Proposed names in the fix
	// item are allowed to be new. This is a syntactic check, not a semantic judge.
	for _, m := range inlineCode.FindAllStringSubmatch(sections[0], -1) {
		term := strings.TrimSuffix(m[1], "()")
		if identifier.MatchString(m[1]) {
			re := regexp.MustCompile(`\b` + regexp.QuoteMeta(term) + `\b`)
			if !re.MatchString(source) {
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
