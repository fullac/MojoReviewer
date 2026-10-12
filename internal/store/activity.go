package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Activity is a receipt for one attempt, separate from SHA/session deduplication.
// It never stores prompts, model output, credentials, or raw errors.
type Activity struct {
	ID         string `json:"id"`
	Repo       string `json:"repo"`
	Number     int    `json:"number"`
	HeadSHA    string `json:"head_sha"`
	State      string `json:"state"`
	Stage      string `json:"stage,omitempty"`
	UpdatedAt  string `json:"updated_at"`
	CommentURL string `json:"comment_url,omitempty"`
}

// Activity records queued/running/terminal states in a bounded, atomic snapshot.
func (s *Store) Activity(a Activity) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(filepath.Dir(s.path), "activity.json")
	var rows []Activity
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &rows); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	a.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	found := false
	for i := range rows {
		if rows[i].ID == a.ID {
			rows[i] = a
			found = true
			break
		}
	}
	if !found {
		rows = append(rows, a)
	}
	if len(rows) > 256 {
		rows = rows[len(rows)-256:]
	}
	raw, err := json.Marshal(rows)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".activity-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// HasActivity makes transport retries of a GitHub delivery idempotent, including
// comment events; fresh comments have new delivery IDs and may review again.
func (s *Store) HasActivity(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(s.path), "activity.json"))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var rows []Activity
	if err := json.Unmarshal(raw, &rows); err != nil {
		return false, err
	}
	for _, row := range rows {
		if row.ID == id {
			return true, nil
		}
	}
	return false, nil
}

// InterruptActivities prevents stale running/queued receipts from a previous
// process being displayed as live when a new reviewer starts.
func (s *Store) InterruptActivities() error {
	s.mu.Lock()
	path := filepath.Join(filepath.Dir(s.path), "activity.json")
	raw, err := os.ReadFile(path)
	s.mu.Unlock()
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var rows []Activity
	if err := json.Unmarshal(raw, &rows); err != nil {
		return err
	}
	for _, a := range rows {
		if a.State == "queued" || a.State == "running" {
			a.State = "interrupted"
			a.Stage = "shutdown"
			if err := s.Activity(a); err != nil {
				return err
			}
		}
	}
	return nil
}
