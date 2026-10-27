package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ReviewContext records Mojo's input at the start of a model attempt. It is
// separate from lightweight activity receipts and is not a provider wire trace.
type ReviewContext struct {
	AttemptID    string `json:"attempt_id"`
	Repo         string `json:"repo"`
	Number       int    `json:"number"`
	SessionID    string `json:"session_id"`
	CreatedAt    string `json:"created_at"`
	MessageStart int    `json:"message_start"`
	SystemPrompt string `json:"system_prompt"`
	TaskPrompt   string `json:"task_prompt"`
	Provider     string `json:"provider"`
	Model        string `json:"model"`
}

func (s *Store) SaveReviewContext(value ReviewContext) error {
	if value.AttemptID == "" || value.SessionID == "" {
		return fmt.Errorf("审查上下文缺少关联 ID")
	}
	value.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	// Delivery IDs are data, never a path component.
	sum := sha256.Sum256([]byte(value.AttemptID))
	dir := filepath.Join(filepath.Dir(s.path), "review_context")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return atomicWrite(filepath.Join(dir, hex.EncodeToString(sum[:])+".json"), raw)
}
