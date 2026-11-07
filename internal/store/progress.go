package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const progressEventLimit = 400

// ProgressEvent is one sanitized step of a review attempt.
// It never stores prompts, diffs, file bodies, credentials, or absolute paths.
type ProgressEvent struct {
	At      string `json:"at"`
	Kind    string `json:"kind"`
	Summary string `json:"summary"`
}

var progressIDPattern = regexp.MustCompile(`^[A-Za-z0-9:_-]{1,80}$`)

func progressPath(dataDir, id string) (string, error) {
	if !progressIDPattern.MatchString(id) || strings.Contains(id, "..") {
		return "", os.ErrNotExist
	}
	return filepath.Join(dataDir, "progress", id+".json"), nil
}

// AppendProgress adds one event for an attempt and keeps the newest events.
func AppendProgress(dataDir, id string, event ProgressEvent) error {
	path, err := progressPath(dataDir, id)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var rows []ProgressEvent
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &rows); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	event.At = time.Now().UTC().Format(time.RFC3339Nano)
	event.Kind = strings.TrimSpace(event.Kind)
	event.Summary = sanitizeProgressSummary(event.Summary)
	if event.Kind == "" || event.Summary == "" {
		return nil
	}
	rows = append(rows, event)
	if len(rows) > progressEventLimit {
		rows = rows[len(rows)-progressEventLimit:]
	}
	raw, err := json.Marshal(rows)
	if err != nil {
		return err
	}
	if err := atomicWrite(path, raw); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// ReadProgress returns the events for one attempt.
// A missing file is an empty list, so older attempts can still open.
func ReadProgress(dataDir, id string) ([]ProgressEvent, error) {
	path, err := progressPath(dataDir, id)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return []ProgressEvent{}, nil
	}
	if err != nil {
		return nil, err
	}
	var rows []ProgressEvent
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, err
	}
	if rows == nil {
		rows = []ProgressEvent{}
	}
	return rows, nil
}

func sanitizeProgressSummary(text string) string {
	text = strings.ReplaceAll(text, "\r", " ")
	text = strings.ReplaceAll(text, "\n", " ")
	text = strings.Join(strings.Fields(text), " ")
	text = strings.ReplaceAll(text, "\\", "/")
	const max = 240
	runes := []rune(text)
	if len(runes) > max {
		text = string(runes[:max])
	}
	return strings.TrimSpace(text)
}
