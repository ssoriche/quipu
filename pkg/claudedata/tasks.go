package claudedata

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// TaskFile is one ~/.claude/tasks/<sessionId>/<n>.json task record. Status
// mapping (Claude's pending/in_progress/completed vocabulary onto quipu's
// own task-status vocabulary) is the caller's job, not this package's.
type TaskFile struct {
	ID          string
	Subject     string
	Description string
	Status      string
}

// ReadSessionTasks reads ~/.claude/tasks/<sessionID>/*.json. Only files
// whose name is <digits>.json are task files; everything else (.lock,
// .highwatermark, and any other non-numeric name) is skipped. A missing
// tasks directory is not an error: it returns (nil, nil). Results are
// sorted by numeric id ascending.
func ReadSessionTasks(home, sessionID string) ([]TaskFile, error) {
	dir := filepath.Join(home, ".claude", "tasks", sessionID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("claudedata: read %s: %w", dir, err)
	}

	var tasks []TaskFile
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		stem, ok := numericStem(name)
		if !ok {
			continue
		}

		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue // unreadable: skip rather than fail the whole scan
		}
		var t struct {
			Subject     string `json:"subject"`
			Description string `json:"description"`
			Status      string `json:"status"`
		}
		if err := json.Unmarshal(data, &t); err != nil {
			continue // malformed: skip
		}
		tasks = append(tasks, TaskFile{
			ID:          stem,
			Subject:     t.Subject,
			Description: t.Description,
			Status:      t.Status,
		})
	}

	sort.Slice(tasks, func(i, j int) bool {
		ni, _ := strconv.Atoi(tasks[i].ID)
		nj, _ := strconv.Atoi(tasks[j].ID)
		return ni < nj
	})
	return tasks, nil
}

// numericStem reports whether name is "<digits>.json", returning the digits.
func numericStem(name string) (string, bool) {
	stem, ok := strings.CutSuffix(name, ".json")
	if !ok || stem == "" {
		return "", false
	}
	for _, c := range stem {
		if c < '0' || c > '9' {
			return "", false
		}
	}
	return stem, true
}
