package collector

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func currentProcessIdentity(t *testing.T) processIdentity {
	t.Helper()
	stat, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(stat)[strings.LastIndexByte(string(stat), ')')+1:])
	ticks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		t.Fatal(err)
	}
	return processIdentity{PID: os.Getpid(), StartTicks: ticks, BootID: strings.TrimSpace(string(boot))}
}

func TestDetachedProcessIdentity(t *testing.T) {
	p := currentProcessIdentity(t)
	if !detachedProcessAlive(p) {
		t.Fatal("current process not detected")
	}
	for _, change := range []func(*processIdentity){
		func(p *processIdentity) { p.PID = -1 },
		func(p *processIdentity) { p.StartTicks++ },
		func(p *processIdentity) { p.StartTicks = 0 },
		func(p *processIdentity) { p.BootID = "previous-boot" },
		func(p *processIdentity) { p.BootID = "" },
	} {
		invalid := p
		change(&invalid)
		if detachedProcessAlive(invalid) {
			t.Fatalf("invalid identity accepted: %+v", invalid)
		}
	}
}

func TestMatchesProcessStat(t *testing.T) {
	for _, state := range []string{"S", "R", "Z", "X"} {
		stat := "123 (command with ) parentheses) " + state + strings.Repeat(" 0", 18) + " 456 0"
		want := state == "S" || state == "R"
		if got := matchesProcessStat(stat, 456); got != want {
			t.Errorf("state %s = %v, want %v", state, got, want)
		}
		if matchesProcessStat(stat, 457) {
			t.Error("reused PID accepted")
		}
	}
	for _, stat := range []string{"", "no parentheses", "123 (short) S", "123 (invalid) S" + strings.Repeat(" 0", 18) + " invalid"} {
		if matchesProcessStat(stat, 456) {
			t.Errorf("invalid stat accepted: %q", stat)
		}
	}
}

func TestDetachedShellSurvivesIdleAndDisconnectedOwner(t *testing.T) {
	for _, live := range []bool{true, false} {
		t.Run(strconv.FormatBool(live), func(t *testing.T) {
			root := t.TempDir()
			id := "00000000-0000-0000-0000-000000000001"
			dir := filepath.Join(root, id)
			p := currentProcessIdentity(t)
			path := writeShellRecord(t, dir, "worker", shellRecord(id, "worker", p))
			old := time.Now().Add(-2 * time.Hour)
			ts := old.UTC().Format(time.RFC3339Nano)
			events := `{"type":"tool.execution_start","timestamp":"` + ts + `","data":{"toolCallId":"b1","toolName":"bash","arguments":{"command":"fabricated-server","mode":"async","detach":true,"shellId":"worker"}}}` + "\n" +
				`{"type":"tool.execution_complete","timestamp":"` + ts + `","data":{"toolCallId":"b1","success":true}}` + "\n" +
				`{"type":"session.task_complete","timestamp":"` + ts + `","data":{}}` + "\n"
			eventsFile := filepath.Join(dir, "events.jsonl")
			if err := os.WriteFile(eventsFile, []byte(events), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(eventsFile, old, old); err != nil {
				t.Fatal(err)
			}
			if live {
				writeLock(t, dir, old.Add(-time.Minute))
			}
			c := &Collector{stateDir: root, sessions: map[string]*sessionEntry{}}
			check := func(wantShells int) {
				t.Helper()
				sessions := c.collectCopilotSessions(time.Now().UnixMilli())
				if !live && wantShells == 0 {
					if len(sessions) != 0 || len(c.sessions) != 0 {
						t.Fatal("inactive owner retained after shell stopped")
					}
					return
				}
				if len(sessions) != 1 {
					t.Fatalf("sessions = %d, want 1", len(sessions))
				}
				s := sessions[0]
				if s.Live != live || s.Busy || s.Waiting || len(s.Subagents) != 0 || len(s.RunningTools) != 0 || len(s.BackgroundShells) != wantShells {
					t.Fatalf("session = %+v", s)
				}
			}
			check(1)
			// Metadata can change without any new session events.
			p.StartTicks++
			writeShellRecord(t, dir, "worker", shellRecord(id, "worker", p))
			check(0)
			p.StartTicks--
			writeShellRecord(t, dir, "worker", shellRecord(id, "worker", p))
			check(1)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			check(0)
		})
	}
}
