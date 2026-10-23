package agent

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// foregroundQueueFile is the on-disk record LuckyAgent keeps per session.
// The filename is sha256(sessionID), matching agent.openForeground.
func foregroundQueueFile(homeDir, sessionID string) string {
	name := fmt.Sprintf("%x.json", sha256.Sum256([]byte(sessionID)))
	return filepath.Join(homeDir, "runtime", "foreground", name)
}

type foregroundQueueFileData struct {
	Tasks []foregroundTaskRecord `json:"tasks"`
}

type foregroundTaskRecord struct {
	ID        string    `json:"id"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
}

// blockedForegroundTask reads the newest blocked foreground task for a session.
// Task IDs come only from this local record, never from a PR comment.
// Missing or unreadable records mean there is nothing safe to resume.
func blockedForegroundTask(homeDir, sessionID string) (string, bool) {
	sessionID = strings.TrimSpace(sessionID)
	homeDir = strings.TrimSpace(homeDir)
	if sessionID == "" || homeDir == "" {
		return "", false
	}
	raw, err := os.ReadFile(foregroundQueueFile(homeDir, sessionID))
	if err != nil || len(raw) == 0 {
		return "", false
	}
	var data foregroundQueueFileData
	if err := json.Unmarshal(raw, &data); err != nil {
		return "", false
	}
	var newest foregroundTaskRecord
	found := false
	for _, task := range data.Tasks {
		if task.State != "blocked" || !foregroundTaskIDPattern.MatchString(task.ID) {
			continue
		}
		if !found || task.CreatedAt.After(newest.CreatedAt) {
			newest = task
			found = true
		}
	}
	if !found {
		return "", false
	}
	return newest.ID, true
}

// resumeForegroundMessage is the exact control text LuckyAgent accepts.
// The runtime matches the whole message: "继续前台任务" or "继续前台任务 tq-N".
// A second line is a new foreground task, so the close-out stays in the
// recovered original prompt (already in the blocked task description).
func resumeForegroundMessage(taskID string) string {
	return "继续前台任务 " + taskID
}
