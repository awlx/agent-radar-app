package collector

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func shellRecord(sessionID, shellID string, p processIdentity) map[string]any {
	return map[string]any{
		"version": 1, "sessionId": sessionID, "shellId": shellID, "state": "active",
		"createdUnixMs": int64(1000), "heartbeatUnixMs": int64(1000),
		"supervisor": p, "owner": processIdentity{PID: -1},
		"logPath": "/private/shell.log", "command": "private-command --secret=example",
	}
}

func writeShellRecord(t *testing.T, dir, name string, record map[string]any) string {
	t.Helper()
	dir = filepath.Join(dir, "detached-shells")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name+".json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestBackgroundShellRecords(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
		alive  bool
		want   int
	}{
		{"live with old heartbeat and dead owner", func(map[string]any) {}, true, 1},
		{"dead supervisor", func(map[string]any) {}, false, 0},
		{"finished", func(r map[string]any) { r["state"] = "completed" }, true, 0},
		{"wrong session", func(r map[string]any) { r["sessionId"] = "other" }, true, 0},
		{"unsupported version", func(r map[string]any) { r["version"] = 2 }, true, 0},
		{"missing ID", func(r map[string]any) { delete(r, "shellId") }, true, 0},
		{"missing start", func(r map[string]any) { delete(r, "createdUnixMs") }, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			p := processIdentity{PID: 123, StartTicks: 456, BootID: "fabricated"}
			record := shellRecord("session", "worker", p)
			tc.change(record)
			writeShellRecord(t, dir, "worker", record)
			got := collectBackgroundShells(dir, "session", func(got processIdentity) bool {
				if got != p {
					t.Fatalf("identity = %+v, want %+v", got, p)
				}
				return tc.alive
			})
			if len(got) != tc.want {
				t.Fatalf("shells = %+v, want %d", got, tc.want)
			}
			if len(got) > 0 && (got[0].ID != "worker" || got[0].PID != p.PID || got[0].StartedAt != 1000) {
				t.Fatalf("shell = %+v", got[0])
			}
			b, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(b), "private") || strings.Contains(string(b), "secret") {
				t.Fatal("shell discovery exposed private metadata")
			}
		})
	}
}

func TestBackgroundShellMalformedAndDuplicateRecords(t *testing.T) {
	dir := t.TempDir()
	for _, id := range []string{"z", "a", "duplicate"} {
		shellID := id
		if id == "duplicate" {
			shellID = "a"
		}
		writeShellRecord(t, dir, id, shellRecord("session", shellID, processIdentity{PID: 123}))
	}
	path := writeShellRecord(t, dir, "malformed", nil)
	if err := os.WriteFile(path, []byte(`{"state":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "detached-shells", "owner.heartbeat"), []byte("not JSON"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := collectBackgroundShells(dir, "session", func(processIdentity) bool { return true })
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "z" {
		t.Fatalf("shells = %+v", got)
	}
	if got := collectBackgroundShells(t.TempDir(), "session", func(processIdentity) bool { return true }); len(got) != 0 {
		t.Fatalf("shells in empty directory = %+v", got)
	}
}

func TestSummaryIncludesShellOnlySessions(t *testing.T) {
	s := &Session{ID: "owner", BackgroundShells: []BackgroundShell{{ID: "worker", PID: 123, StartedAt: 1000}}}
	sum := Summarize(&Snapshot{Sessions: []*Session{s}}, true)
	if len(sum.Sessions) != 1 || sum.Sessions[0].Status != "idle" ||
		len(sum.Sessions[0].Subagents) != 0 || len(sum.Sessions[0].RunningTools) != 0 ||
		len(sum.Sessions[0].BackgroundShells) != 1 {
		t.Fatalf("summary = %+v", sum)
	}
}
