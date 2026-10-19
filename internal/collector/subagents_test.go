package collector

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func subEvent(t *testing.T, typ, agent string, at time.Time, data map[string]any) string {
	t.Helper()
	b, err := json.Marshal(struct {
		Type      string         `json:"type"`
		AgentID   string         `json:"agentId"`
		Timestamp string         `json:"timestamp"`
		Data      map[string]any `json:"data"`
	}{typ, agent, at.UTC().Format(time.RFC3339Nano), data})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCompletedBackgroundAgentResumesWithoutAnotherLifecycleStart(t *testing.T) {
	p := newParseState()
	now := time.Now()
	apply := func(typ string, seconds int, data map[string]any) {
		p.apply(ev(t, subEvent(t, typ, "worker", now.Add(time.Duration(seconds)*time.Second), data)))
	}
	apply("subagent.started", -3600, map[string]any{"toolCallId": "task", "agentDisplayName": "Worker", "executionMode": "background"})
	apply("subagent.completed", -3500, map[string]any{"toolCallId": "task"})
	apply("user.message", -10, map[string]any{"content": "Inspect another fabricated fixture"})
	apply("assistant.turn_start", -9, nil)
	a := p.subs["worker"]
	if !a.Running || a.EndedAt != 0 || a.Mode != "background" || len(p.subOrder) != 1 {
		t.Fatalf("completed agent did not resume under its original identity: %+v", a)
	}
	apply("tool.execution_start", -8, map[string]any{"parentToolCallId": "task", "toolCallId": "scan", "toolName": "grep", "arguments": map[string]any{"pattern": "example"}})
	apply("assistant.turn_end", -7, nil)
	if !a.Running || p.toolStarts["scan"].AgentID != "worker" {
		t.Fatal("pending subagent tool must keep the resumed agent running")
	}
	apply("tool.execution_complete", -6, map[string]any{"toolCallId": "scan", "success": true})
	if a.Running || a.EndedAt != now.Add(-6*time.Second).UnixMilli() {
		t.Fatalf("resumed work did not finish at the new activity boundary: %+v", a)
	}
	apply("assistant.turn_start", -5, nil)
	p.apply(ev(t, subEvent(t, "assistant.turn_end", "", now.Add(-4*time.Second), nil)))
	if !a.Running {
		t.Fatal("parent turn end stopped the subagent")
	}
	apply("assistant.turn_end", -3, nil)
	if a.Running {
		t.Fatal("resumed subagent stayed running after its own final turn")
	}
	apply("assistant.turn_start", -2, nil)
	apply("session.idle", -1, nil)
	if a.Running {
		t.Fatal("scoped idle did not stop the subagent")
	}
}

func TestAllRecordedSubagentsRetainedAndResumedAgentCountedAsBusy(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, testChildID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	var lines []string
	for i := 0; i < 4; i++ {
		id := fmt.Sprintf("worker-%d", i)
		lines = append(lines,
			subEvent(t, "subagent.started", id, now.Add(-2*time.Hour), map[string]any{"toolCallId": "task-" + id, "agentDisplayName": id, "executionMode": "background"}),
			subEvent(t, "subagent.completed", id, now.Add(-time.Hour), map[string]any{"toolCallId": "task-" + id}),
		)
	}
	lines = append(lines,
		subEvent(t, "user.message", "worker-3", now.Add(-3*time.Second), nil),
		subEvent(t, "assistant.turn_start", "worker-3", now.Add(-2*time.Second), nil),
	)
	file := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(file, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeLock(t, dir, now.Add(-3*time.Hour))
	c := &Collector{stateDir: root, sessions: map[string]*sessionEntry{}}
	s := c.collectCopilotSession(testChildID, now.UnixMilli())
	if s == nil || !s.Busy || len(s.Subagents) != 4 {
		t.Fatalf("want four recorded agents and a busy owner: %+v", s)
	}
	running := 0
	for _, a := range s.Subagents {
		if a.Running {
			running++
			if a.ID != "worker-3" {
				t.Fatalf("wrong agent reactivated: %+v", a)
			}
		}
	}
	if running != 1 {
		t.Fatalf("running=%d, want 1", running)
	}
	snap := &Snapshot{Now: now.UnixMilli(), Sessions: []*Session{s}}
	if got := len(Summarize(snap, false).Sessions[0].Subagents); got != 4 {
		t.Fatalf("all snapshot subagents=%d, want 4", got)
	}
	if got := len(Summarize(snap, true).Sessions[0].Subagents); got != 1 {
		t.Fatalf("active snapshot subagents=%d, want 1", got)
	}
}

func TestColdTailRestoresResumedBackgroundTurnsAndPendingTools(t *testing.T) {
	now := time.Now()
	head := []string{
		subEvent(t, "subagent.started", "worker", now.Add(-time.Hour), map[string]any{"toolCallId": "task", "executionMode": "background"}),
		subEvent(t, "subagent.completed", "worker", now.Add(-50*time.Minute), map[string]any{"toolCallId": "task"}),
		subEvent(t, "user.message", "worker", now.Add(-10*time.Second), nil),
		subEvent(t, "assistant.turn_start", "worker", now.Add(-9*time.Second), nil),
		subEvent(t, "tool.execution_start", "worker", now.Add(-8*time.Second), map[string]any{"toolCallId": "scan", "parentToolCallId": "task", "toolName": "grep"}),
		subEvent(t, "assistant.turn_end", "worker", now.Add(-7*time.Second), nil),
	}
	data := strings.Join(head, "\n") + "\n" +
		`{"type":"other","data":"` + strings.Repeat("x", initialTail) + `"}` + "\n" +
		subEvent(t, "assistant.turn_end", "", now.Add(-time.Second), nil) + "\n"
	file := filepath.Join(t.TempDir(), "events.jsonl")
	if err := os.WriteFile(file, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	entry := &sessionEntry{}
	if err := entry.readAppended(file, st.Size(), fileIno(st)); err != nil {
		t.Fatal(err)
	}
	a := entry.p.subs["worker"]
	if !a.Running || a.EndedAt != 0 || entry.p.toolStarts["scan"] == nil || entry.p.toolStarts["scan"].AgentID != a.ID {
		t.Fatalf("resumed tool before tail lost: %+v", a)
	}
	entry.p.apply(ev(t, subEvent(t, "tool.execution_complete", "worker", now, map[string]any{"toolCallId": "scan", "success": true})))
	if a.Running {
		t.Fatal("completed historical tool left agent running")
	}
}
