package projectmem

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// FileState 记录单个知识文件上次索引时的大小与 mtime。
type FileState struct {
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

// RepoState 是某个仓库的项目记忆索引状态。
type RepoState struct {
	Repo       string               `json:"repo"`
	HeadSHA    string               `json:"head_sha"`
	UpdatedAt  time.Time            `json:"updated_at"`
	Indexed    map[string]FileState `json:"indexed"`
	SummaryKey string               `json:"summary_key,omitempty"`
}

// Store 按仓库保存索引元数据，路径通常是 data_dir/project_memory.json。
type Store struct {
	path string
	mu   sync.Mutex
	data map[string]RepoState
}

// Open 打开或创建状态文件。
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	s := &Store{path: path, data: map[string]RepoState{}}
	raw, err := os.ReadFile(path)
	if err == nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, &s.data); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// Get 返回仓库状态。
func (s *Store) Get(repo string) (RepoState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.data[repo]
	if !ok {
		return RepoState{}, false
	}
	if st.Indexed == nil {
		st.Indexed = map[string]FileState{}
	}
	return st, true
}

// Put 保存仓库状态。
func (s *Store) Put(repo string, st RepoState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st.Indexed == nil {
		st.Indexed = map[string]FileState{}
	}
	st.Repo = repo
	st.UpdatedAt = time.Now().UTC()
	s.data[repo] = st
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, append(raw, '\n'), 0o600)
}
