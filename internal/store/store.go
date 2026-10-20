package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync"
)

// Record 记住一个 PR 审查到哪个提交、用哪个 LuckyAgent 会话。
type Record struct {
	Repo      string `json:"repo"`
	Number    int    `json:"number"`
	HeadSHA   string `json:"head_sha"`
	SessionID string `json:"session_id"`
	// AutoHeads records started automatic attempts, independently of the latest
	// session head. Explicit comment/GUI reviews do not consume another attempt.
	AutoHeads []string `json:"auto_heads"`
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
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err == nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, &s.data); err != nil {
			return nil, err
		}
	}
	if s.data == nil {
		s.data = map[string]Record{}
	}
	for key, rec := range s.data {
		// Legacy records used HeadSHA as the automatic-attempt reservation.
		if rec.AutoHeads == nil && rec.HeadSHA != "" {
			rec.AutoHeads = []string{rec.HeadSHA}
			s.data[key] = rec
		}
	}
	return s, nil
}

// Get 返回已有记录。
func (s *Store) Get(key string) (Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.data[key]
	rec.AutoHeads = slices.Clone(rec.AutoHeads)
	return rec, ok
}

// HasAutoHead reports whether an automatic attempt for this PR/head started.
func (s *Store) HasAutoHead(key, sha string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Contains(s.data[key].AutoHeads, sha)
}

// ReserveAutoHead persists the reservation before checkout/model work starts.
// Failed attempts stay reserved; explicit comments/GUI actions may retry them.
func (s *Store) ReserveAutoHead(key string, rec Record) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing := s.data[key]
	if slices.Contains(existing.AutoHeads, rec.HeadSHA) {
		return false, nil
	}
	rec.AutoHeads = append(slices.Clone(existing.AutoHeads), rec.HeadSHA)
	if rec.SessionID == "" {
		rec.SessionID = existing.SessionID
	}
	return true, s.putLocked(key, rec)
}

// Put 保存记录。同一个提交重复审查时仍更新会话 ID。
func (s *Store) Put(key string, rec Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Session updates must not discard reservations for older heads.
	rec.AutoHeads = slices.Clone(s.data[key].AutoHeads)
	if rec.AutoHeads == nil {
		rec.AutoHeads = []string{}
	}
	return s.putLocked(key, rec)
}

func (s *Store) putLocked(key string, rec Record) error {
	old, existed := s.data[key]
	committed := false
	defer func() {
		if !committed {
			if existed {
				s.data[key] = old
			} else {
				delete(s.data, key)
			}
		}
	}()
	s.data[key] = rec
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	if err := atomicWrite(s.path, append(raw, '\n')); err != nil {
		return err
	}
	committed = true
	return nil
}

func atomicWrite(path string, raw []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
