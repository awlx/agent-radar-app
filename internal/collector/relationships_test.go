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

const (
	testParentID  = "00000000-0000-0000-0000-000000000001"
	testChildID   = "00000000-0000-0000-0000-000000000002"
	testProjectID = "00000000-0000-0000-0000-000000000003"
	testAppID     = "00000000-0000-0000-0000-000000000004"
)

func TestCreatedSessionIdentity(t *testing.T) {
	cases := []struct {
		name    string
		result  any
		id      string
		created bool
	}{
		{"plain creation", map[string]any{"content": "Created session 'Example'\nid: " + testChildID + "\nurl: copilot://sessions/" + testChildID}, testChildID, true},
		{"app creation sentence", map[string]any{"content": "Created session 'Example' (id: " + testChildID + ") in project 'Demo'. Open it: copilot://sessions/" + testChildID}, testChildID, true},
		{"UUID in session name is not the session ID", map[string]any{"content": "Created session 'Example " + testProjectID + "' (id: " + testChildID + ") in project 'Demo'."}, testChildID, true},
		{"project precedes session", map[string]any{"project_id": testProjectID, "session_id": testChildID, "created": true}, testChildID, true},
		{"paired namespace", map[string]any{"id": testAppID, "copilot_session_id": testChildID, "created": true}, testChildID, true},
		{"MCP content blocks", map[string]any{"content": []any{map[string]any{"type": "text", "text": `{"sessionId":"` + testChildID + `","created":true}`}}}, testChildID, true},
		{"existing session", map[string]any{"session_id": testChildID, "created": false}, testChildID, false},
		{"unrelated UUID", map[string]any{"content": "Project " + testProjectID + " not found"}, "", false},
		{"nested prompt UUID", map[string]any{"arguments": map[string]any{"id": testProjectID}}, "", false},
		{"error", map[string]any{"isError": true, "session_id": testChildID}, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw, err := json.Marshal(c.result)
			if err != nil {
				t.Fatal(err)
			}
			got, created := createdSession(raw)
			if got.id != c.id || created != c.created {
				t.Fatalf("createdSession = %+v, %v; want %s, %v", got, created, c.id, c.created)
			}
		})
	}
}

func creationEvents(t *testing.T, p *parseState, tool string, args map[string]any, result any, success bool) {
	t.Helper()
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	for _, data := range []map[string]any{
		{"toolCallId": "create-1", "toolName": tool, "arguments": args},
		{"toolCallId": "create-1", "result": result, "success": success},
	} {
		typ := "tool.execution_start"
		if _, ok := data["result"]; ok {
			typ = "tool.execution_complete"
		}
		raw, err := json.Marshal(map[string]any{"type": typ, "timestamp": ts, "data": data})
		if err != nil {
			t.Fatal(err)
		}
		p.apply(ev(t, string(raw)))
	}
}

func TestOwnershipIndependentOfMessagesAndSuccessfulCreation(t *testing.T) {
	for _, c := range []struct {
		name, tool string
		args       map[string]any
		result     any
		success    bool
		want       int
	}{
		{"created child", "create_session", nil, map[string]any{"session_id": testChildID}, true, 1},
		{"failed creation", "create_session", nil, map[string]any{"session_id": testChildID}, false, 0},
		{"detached session", "create_session", map[string]any{"detached": true}, map[string]any{"session_id": testChildID}, true, 0},
		{"uncoordinated nested session", "create_session", map[string]any{"coordinate_with_creator": false}, map[string]any{"session_id": testChildID}, true, 1},
		{"reuse open PR", "open_pr_session", nil, map[string]any{"session_id": testChildID, "created": false}, true, 0},
		{"create open issue", "open_issue_session", nil, map[string]any{"content": "Created session 'Example'\nid: " + testChildID}, true, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := newParseState()
			creationEvents(t, p, c.tool, c.args, c.result, c.success)
			for i := 0; i < maxPeers+20; i++ {
				p.apply(ev(t, fmt.Sprintf(`{"type":"tool.execution_start","data":{"toolCallId":"message-%d","toolName":"send_session_message","arguments":{"session_id":"%s"}}}`, i, testChildID)))
			}
			if len(p.children) != c.want || len(p.peers) > maxPeers {
				t.Fatalf("children = %+v, peers = %d", p.children, len(p.peers))
			}
		})
	}
}

func TestResolveSessionAliasesWithoutDuplicateNodes(t *testing.T) {
	p := newParseState()
	creationEvents(t, p, "create_session", nil, map[string]any{
		"id": testAppID, "project_id": testProjectID, "copilot_session_id": testChildID,
	}, true)
	parent := &Session{ID: testParentID, Peers: []Peer{{To: testAppID, Kind: "message"}}}
	child := &Session{ID: testChildID}
	sessions := []*Session{parent, child}
	resolveSessionRelations(sessions, map[string]*sessionEntry{testParentID: {p: p}})
	if len(sessions) != 2 || len(parent.ChildSessions) != 1 || parent.ChildSessions[0] != testChildID || parent.Peers[0].To != testChildID {
		t.Fatalf("children = %v, peers = %+v", parent.ChildSessions, parent.Peers)
	}
	// A message alone never supplies ownership.
	other := &Session{ID: testProjectID, Peers: []Peer{{To: testChildID, Kind: "message"}}}
	resolveSessionRelations([]*Session{other, child}, map[string]*sessionEntry{testProjectID: {p: newParseState()}})
	if len(other.ChildSessions) != 0 {
		t.Fatalf("message became ownership: %v", other.ChildSessions)
	}
}

func TestOwnershipRecoveredBeforeInitialTail(t *testing.T) {
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	head := `{"type":"tool.execution_start","timestamp":"` + ts + `","data":{"toolCallId":"create","toolName":"create_session","arguments":{}}}` + "\n" +
		`{"type":"tool.execution_complete","timestamp":"` + ts + `","data":{"toolCallId":"create","success":true,"result":{"content":"Created session 'Example' (id: ` + testChildID + `) in project 'Demo'."}}}` + "\n"
	data := head + `{"type":"other","data":"` + strings.Repeat("x", initialTail) + `"}` + "\n" +
		`{"type":"assistant.turn_end","timestamp":"` + ts + `","data":{}}` + "\n"
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
	if _, ok := entry.p.children[testChildID]; !ok {
		t.Fatalf("ownership before initial tail lost: %+v", entry.p.children)
	}
	if len(entry.p.toolStarts) != 0 || entry.p.turnOpen {
		t.Fatalf("historical creation became pending activity: %+v", entry.p)
	}
}

func TestCollectedParentChildSubagentChainSurvivesIdleTransitions(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	old := now.Add(-24 * time.Hour).UTC().Format(time.RFC3339Nano)
	current := now.UTC().Format(time.RFC3339Nano)
	parentLog := `{"type":"tool.execution_start","timestamp":"` + old + `","data":{"toolCallId":"create","toolName":"create_session"}}` + "\n" +
		`{"type":"tool.execution_complete","timestamp":"` + old + `","data":{"toolCallId":"create","success":true,"result":{"content":"Created session 'Child' (id: ` + testChildID + `) in project 'Demo'."}}}` + "\n" +
		`{"type":"assistant.turn_end","timestamp":"` + old + `","data":{}}` + "\n"
	childLog := `{"type":"subagent.started","agentId":"worker-id","timestamp":"` + current + `","data":{"toolCallId":"task-call","agentDisplayName":"Worker"}}` + "\n" +
		`{"type":"tool.execution_start","agentId":"worker-id","timestamp":"` + current + `","data":{"parentToolCallId":"task-call","toolCallId":"scan","toolName":"grep","arguments":{"pattern":"example"}}}` + "\n"
	for id, log := range map[string]string{testParentID: parentLog, testChildID: childLog} {
		dir := filepath.Join(root, id)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(log), 0o600); err != nil {
			t.Fatal(err)
		}
		writeLock(t, dir, now.Add(-time.Hour))
	}
	c := &Collector{stateDir: root, sessions: map[string]*sessionEntry{}}
	sessions := c.collectCopilotSessions(now.UnixMilli())
	var parent, child *Session
	for _, s := range sessions {
		if s.ID == testParentID {
			parent = s
		} else if s.ID == testChildID {
			child = s
		}
	}
	if parent == nil || parent.Busy || len(parent.ChildSessions) != 1 || parent.ChildSessions[0] != testChildID {
		t.Fatalf("idle creator relationship not retained: %+v", parent)
	}
	if child == nil || !child.Busy || len(child.Subagents) != 1 || child.Subagents[0].ID != "worker-id" ||
		child.RunningTools[0].AgentID != "worker-id" || child.Subagents[0].Doing != "grep: example" {
		t.Fatalf("child subagent attribution lost: %+v", child)
	}
	f, err := os.OpenFile(filepath.Join(root, testChildID, "events.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(`{"type":"subagent.completed","agentId":"worker-id","timestamp":"` + current + `","data":{"toolCallId":"task-call"}}` + "\n")
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range c.collectCopilotSessions(now.UnixMilli()) {
		if s.ID == testChildID && (s.Busy || s.Subagents[0].Running) {
			t.Fatalf("completed subagent left child busy: %+v", s)
		}
		if s.ID == testParentID && len(s.ChildSessions) != 1 {
			t.Fatal("ownership disappeared on completion")
		}
	}
}
