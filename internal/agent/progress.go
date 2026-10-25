package agent

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

type readKey struct {
	kind, path string
	start, end int
}

// readProgress 只存一次只读审查的读取回执，不保存代码或跨提交复用结果。
// 内容去重按分工算（同一会话内重复读同一件才给已读提示），sink 只汇总本轮
// 所有分工的回执：把内容去重也做成全局，另一路会被告知「已读」却从没见过内容，
// 于是反复重试到超时。
type readProgress struct {
	mu         sync.Mutex
	seen       map[readKey]bool
	duplicates int
	sink       *readProgress
}

// record 把一次读取记到汇总回执上。
func (p *readProgress) record(key readKey) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.seen == nil {
		p.seen = make(map[readKey]bool)
	}
	p.seen[key] = true
}

func (p *readProgress) countDuplicate() {
	p.mu.Lock()
	p.duplicates++
	p.mu.Unlock()
	if sink := p.sink; sink != nil {
		sink.mu.Lock()
		sink.duplicates++
		sink.mu.Unlock()
	}
}

func (p *readProgress) read(key readKey, fetch func() (string, error)) (string, error) {
	if p == nil {
		return fetch()
	}
	p.mu.Lock()
	if p.seen[key] {
		p.mu.Unlock()
		p.countDuplicate()
		return fmt.Sprintf("本轮已读，未再次读取：%s %q [%d:%d]。请使用此前结果，读取未读文件/行段，或调用 review_note 记录有证据的问题后结束；证据不足时说明未覆盖范围。", key.kind, key.path, key.start, key.end), nil
	}
	out, err := fetch()
	if err == nil {
		if p.seen == nil {
			p.seen = make(map[readKey]bool)
		}
		p.seen[key] = true
	}
	p.mu.Unlock()
	if err == nil {
		p.sink.record(key)
	}
	return out, err
}

func (p *readProgress) summary() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	patches, files := map[string]bool{}, map[string]bool{}
	pages, ranges := 0, 0
	for key := range p.seen {
		switch key.kind {
		case "pr_file_diff":
			patches[key.path] = true
			files[key.path] = true
		case "read_repo_file":
			files[key.path] = true
			ranges++
		case "pr_diff_stat":
			pages++
		}
	}
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	var b strings.Builder
	fmt.Fprintf(&b, "读取回执：文件列表 %d 页，patch %d 个文件，代码 %d 次行段请求；已跳过重复读取 %d 次。读取不代表已完成审查。", pages, len(patches), ranges, p.duplicates)
	for i, path := range paths {
		line := fmt.Sprintf("\n- %q", path)
		if i >= 20 || b.Len()+len(line) > 3000 {
			fmt.Fprintf(&b, "\n其余 %d 个文件未在此展开。", len(paths)-i)
			break
		}
		b.WriteString(line)
	}
	return b.String()
}

func readPath(root, path string) string {
	root, _ = filepath.Abs(root)
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	rel, err := filepath.Rel(root, filepath.Clean(path))
	if err != nil {
		return path
	}
	return filepath.ToSlash(rel)
}
