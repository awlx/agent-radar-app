package collector

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// BackgroundShell is a detached process, not an agent or a pending tool call.
type BackgroundShell struct {
	ID        string `json:"id"`
	PID       int    `json:"pid"`
	StartedAt int64  `json:"startedAt"`
}

type processIdentity struct {
	PID        int    `json:"pid"`
	StartTicks uint64 `json:"startTicks"`
	BootID     string `json:"bootId"`
}

func collectBackgroundShells(dir, sessionID string, alive func(processIdentity) bool) []BackgroundShell {
	dir = filepath.Join(dir, "detached-shells")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var shells []BackgroundShell
	seen := map[string]bool{}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		f, err := os.Open(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		var record struct {
			Version       int             `json:"version"`
			SessionID     string          `json:"sessionId"`
			ShellID       string          `json:"shellId"`
			State         string          `json:"state"`
			CreatedUnixMS int64           `json:"createdUnixMs"`
			Supervisor    processIdentity `json:"supervisor"`
		}
		err = json.NewDecoder(io.LimitReader(f, 64<<10)).Decode(&record)
		f.Close()
		if err != nil || record.Version != 1 || record.SessionID != sessionID ||
			record.ShellID == "" || record.State != "active" || record.CreatedUnixMS <= 0 ||
			seen[record.ShellID] || !alive(record.Supervisor) {
			continue
		}
		// Heartbeats belong to the owner, and may stop while the detached process survives.
		seen[record.ShellID] = true
		shells = append(shells, BackgroundShell{ID: record.ShellID, PID: record.Supervisor.PID, StartedAt: record.CreatedUnixMS})
	}
	sort.Slice(shells, func(i, j int) bool {
		if shells[i].StartedAt != shells[j].StartedAt {
			return shells[i].StartedAt < shells[j].StartedAt
		}
		return shells[i].ID < shells[j].ID
	})
	return shells
}
