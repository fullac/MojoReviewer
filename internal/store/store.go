package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Record 记住一个 PR 审查到哪个提交、用哪个 LuckyAgent 会话。
type Record struct {
	Repo      string `json:"repo"`
	Number    int    `json:"number"`
	HeadSHA   string `json:"head_sha"`
	SessionID string `json:"session_id"`
}

// Store 是一个 JSON 文件，按 PR 保存最近一次审查。
type Store struct {
	path string
	mu   sync.Mutex
	data map[string]Record
}

// Open 打开或创建存储文件。
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	s := &Store{path: path, data: map[string]Record{}}
	raw, err := os.ReadFile(path)
	if err == nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, &s.data); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// Get 返回已有记录。
func (s *Store) Get(key string) (Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.data[key]
	return rec, ok
}

// Put 保存记录。同一个提交重复审查时仍更新会话 ID。
func (s *Store) Put(key string, rec Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = rec
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, append(raw, '\n'), 0o600)
}
