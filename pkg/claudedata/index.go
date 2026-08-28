package claudedata

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// IndexEntry is one entry of a project's sessions-index.json: a fallback
// source of facts for sessions whose jsonl transcript has been pruned.
type IndexEntry struct {
	SessionID   string
	FirstPrompt string
	Summary     string
	GitBranch   string
	Modified    string
}

// ReadSessionsIndex reads <projectDir>/sessions-index.json. A missing file
// is not an error: it returns (nil, nil), since most projects never have
// pruned sessions to fall back on.
func ReadSessionsIndex(projectDir string) ([]IndexEntry, error) {
	path := filepath.Join(projectDir, "sessions-index.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("claudedata: read %s: %w", path, err)
	}

	var doc struct {
		Entries []struct {
			SessionID   string `json:"sessionId"`
			FirstPrompt string `json:"firstPrompt"`
			Summary     string `json:"summary"`
			GitBranch   string `json:"gitBranch"`
			Modified    string `json:"modified"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("claudedata: parse %s: %w", path, err)
	}

	entries := make([]IndexEntry, 0, len(doc.Entries))
	for _, e := range doc.Entries {
		entries = append(entries, IndexEntry{
			SessionID:   e.SessionID,
			FirstPrompt: e.FirstPrompt,
			Summary:     e.Summary,
			GitBranch:   e.GitBranch,
			Modified:    e.Modified,
		})
	}
	return entries, nil
}
