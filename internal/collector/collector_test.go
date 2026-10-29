package collector

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestEventType(t *testing.T) {
	if got := eventType([]byte(`{"type":"session.usage_record","data":{}}`)); got != "session.usage_record" {
		t.Fatalf("got %q", got)
	}
	if got := eventType([]byte(`{"data":{"x":1}}`)); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestClassifyAndDescribe(t *testing.T) {
	cases := []struct{ tool, server, cmd, want string }{
		{"bash", "", "ls", "Shell"},
		{"bash", "", "cd x && cat notes.txt", "Shell"},
		{"bash", "", "gh pr list", "GitHub"},
		{"view", "", "", "Filesystem"},
		{"web_search", "", "", "Web"},
		{"web_search", "github-mcp-server", "", "MCP: github-mcp-server"},
		{"bash", "remote", "gh pr list", "MCP: remote"},
		{"notes-notes_add", "notes", "", "MCP: notes"},
		{"tickets-mcp-server-search", "tickets-mcp-server", "", "MCP: tickets-mcp-server"},
		{"tickets-mcp-server-search", "", "", "MCP: tickets-mcp-server"},
		{"foo-mcp-list", "", "", "MCP: foo-mcp"},
		{"foo-bar", "", "", "MCP: unknown server"},
		{"-bar", "", "", ""},
	}
	for _, c := range cases {
		if got := classifyTool(c.tool, map[string]any{"command": c.cmd}, c.server, nil); got != c.want {
			t.Errorf("classifyTool(%q, %q) = %q, want %q", c.tool, c.server, got, c.want)
		}
	}
	if got := describeArgs(map[string]any{"command": "ls\npwd"}); !strings.HasPrefix(got, "$ ls") {
		t.Errorf("describeArgs = %q", got)
	}
}

func TestMCPServerOf(t *testing.T) {
	known := map[string]bool{"notes": true, "notes-admin": true, "weather": true}
	cases := []struct{ name, want string }{
		{"notes-notes_add", "notes"},
		{"notes-admin-list_users", "notes-admin"},
		{"weather-get_forecast", "weather"},
		{"tickets-mcp-server-search", "tickets-mcp-server"},
		{"github-mcp-get_me", "github-mcp"},
		{"other-tool", ""},
	}
	for _, c := range cases {
		if got := mcpServerOf(c.name, known); got != c.want {
			t.Errorf("mcpServerOf(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestKnownMCPServersFromEvents(t *testing.T) {
	p := newParseState()
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	p.apply(ev(t, `{"type":"assistant.message","timestamp":"`+ts+`","data":{"toolRequests":[{"name":"x"},"bad",{"mcpServerName":"my-srv"}]}}`))
	p.apply(ev(t, `{"type":"tool.execution_start","timestamp":"`+ts+`","data":{"toolCallId":"t0","toolName":"weather-get_forecast","mcpServerName":"weather"}}`))
	p.apply(ev(t, `{"type":"tool.execution_start","timestamp":"`+ts+`","data":{"toolCallId":"t1","toolName":"my-srv-do_thing"}}`))
	p.apply(ev(t, `{"type":"tool.execution_start","timestamp":"`+ts+`","data":{"toolCallId":"t2","toolName":"weather-get_alerts"}}`))
	p.apply(ev(t, `{"type":"tool.execution_start","timestamp":"`+ts+`","data":{"toolCallId":"t3","toolName":"other-thing"}}`))
	for id, want := range map[string]string{"t1": "MCP: my-srv", "t2": "MCP: weather", "t3": "MCP: unknown server"} {
		if got := p.toolStarts[id].Sink; got != want {
			t.Errorf("sink of %s = %q, want %q", id, got, want)
		}
	}
}

func TestParseYAMLBlock(t *testing.T) {
	y := parseYAML("id: abc\nname: |-\n  first line\n  second\nclient_name: \"github/autopilot\"\n")
	if y["name"] != "first line second" || y["client_name"] != "github/autopilot" || y["id"] != "abc" {
		t.Fatalf("parseYAML = %#v", y)
	}
}

func ev(t *testing.T, s string) *rawEvent {
	t.Helper()
	var e rawEvent
	if err := json.Unmarshal([]byte(s), &e); err != nil {
		t.Fatal(err)
	}
	return &e
}

func TestApplyUsageBYOKAndSubagents(t *testing.T) {
	p := newParseState()
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	p.apply(ev(t, `{"type":"session.usage_record","timestamp":"`+ts+`","data":{"usage":{"model":"x","modelDisplayName":"00000000-0000-0000-0000-000000000000/example-model","inputTokens":100,"outputTokens":20,"isByok":true}}}`))
	mt := p.modelTokens["example-model · BYOK"]
	if mt == nil || mt.In != 100 || mt.Out != 20 || mt.Calls != 1 {
		t.Fatalf("modelTokens = %#v", p.modelTokens)
	}
	p.apply(ev(t, `{"type":"subagent.started","timestamp":"`+ts+`","data":{"toolCallId":"c1","agentDisplayName":"Explore","agentName":"explore"}}`))
	p.apply(ev(t, `{"type":"tool.execution_start","timestamp":"`+ts+`","data":{"parentToolCallId":"c1","toolCallId":"t1","toolName":"grep","arguments":{"pattern":"foo"}}}`))
	if s := p.subs["c1"]; s == nil || !s.Running || p.toolStarts["t1"].AgentID != "c1" {
		t.Fatalf("subagent not tracked: %#v", p.subs)
	}
	p.apply(ev(t, `{"type":"subagent.completed","timestamp":"`+ts+`","data":{"toolCallId":"c1"}}`))
	if p.subs["c1"].Running || len(p.toolStarts) != 0 {
		t.Fatalf("subagent not completed")
	}
}

func TestPrescanSubagentsBeforeTail(t *testing.T) {
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	var b strings.Builder
	b.WriteString(`{"type":"subagent.started","agentId":"a1","timestamp":"` + ts + `","data":{"toolCallId":"c1","agentDisplayName":"sub-01"}}` + "\n")
	b.WriteString(`{"type":"tool.execution_start","data":{"big":"` + strings.Repeat("x", 3<<20) + `"}}` + "\n")
	head := int64(b.Len())
	b.WriteString(`{"type":"tool.execution_start","agentId":"a1","timestamp":"` + ts + `","data":{"toolCallId":"t1","toolName":"bash","arguments":{"command":"ls"}}}` + "\n")
	f := t.TempDir() + "/events.jsonl"
	if err := os.WriteFile(f, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	p := newParseState()
	if boundary, err := prescanSubagents(f, p, head); err != nil || boundary != head {
		t.Fatal(boundary, err)
	}
	if s := p.subs["a1"]; s == nil || s.Name != "sub-01" || !s.Running {
		t.Fatalf("subs = %#v", p.subs)
	}
}

func replay(t *testing.T, lines ...string) any {
	t.Helper()
	var state any = map[string]any{}
	for _, l := range lines {
		var m vscodeMutation
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatal(err)
		}
		state = mutate(state, &m)
	}
	return state
}

func TestMutateReplay(t *testing.T) {
	s := replay(t,
		`{"kind":0,"v":{"customTitle":"a","requests":[{"id":"r0"}]}}`,
		`{"kind":1,"k":["customTitle"],"v":"b"}`,
		`{"kind":2,"k":["requests"],"v":[{"id":"r1"},{"id":"r2"}]}`,
		`{"kind":2,"k":["requests"],"v":[{"id":"r3"}],"i":1}`,
		`{"kind":1,"k":["requests",1,"response"],"v":[]}`,
		`{"kind":2,"k":["requests",1,"response"],"v":[{"value":"hi"}]}`,
		`{"kind":1,"k":["inputState","selectedModel","identifier"],"v":"copilot/gpt-5"}`,
		`{"kind":1,"k":["tmp"],"v":1}`,
		`{"kind":3,"k":["tmp"]}`,
	)
	m := asMap(s)
	reqs := asSlice(m["requests"])
	if m["customTitle"] != "b" || len(reqs) != 2 || get(reqs[1], "id") != "r3" {
		t.Fatalf("state = %#v", m)
	}
	if get(asSlice(get(reqs[1], "response"))[0], "value") != "hi" {
		t.Fatalf("response = %#v", reqs[1])
	}
	if modelName(get(m, "inputState", "selectedModel", "identifier")) != "gpt-5" {
		t.Fatalf("inputState = %#v", m["inputState"])
	}
	if _, ok := m["tmp"]; ok {
		t.Fatalf("tmp not deleted")
	}
	if s := replay(t, `{"kind":2,"k":["requests"],"v":[{"id":"x"}],"i":0}`); len(asSlice(get(s, "requests"))) != 1 {
		t.Fatalf("append to missing array = %#v", s)
	}
}

func TestClassifyVSCodeTool(t *testing.T) {
	tool := func(s string) map[string]any {
		var m map[string]any
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	cases := []struct{ tool, want string }{
		{`{"toolId":"mcp_github_list_prs","source":{"type":"mcp","label":"GitHub MCP"}}`, "MCP: GitHub MCP"},
		{`{"toolId":"mcp_playwright_browser_click"}`, "MCP: playwright"},
		{`{"toolId":"run_in_terminal","toolSpecificData":{"kind":"terminal","commandLine":{"original":"git status"}}}`, "Git"},
		{`{"toolId":"run_in_terminal","toolSpecificData":{"kind":"terminal","commandLine":{"original":"ls -la"}}}`, "Shell"},
		{`{"toolId":"copilot_readFile"}`, "Filesystem"},
		{`{"toolId":"manage_todo_list"}`, ""},
		{`{"toolId":"runSubagent","toolSpecificData":{"kind":"subagent"}}`, ""},
	}
	for _, c := range cases {
		if got := classifyVSCodeTool(tool(c.tool)); got != c.want {
			t.Errorf("classifyVSCodeTool(%s) = %q, want %q", c.tool, got, c.want)
		}
	}
}

func TestVSCodeChatWaitingAndBusy(t *testing.T) {
	now := time.Now().UnixMilli()
	ts := strconv.FormatInt(now-5000, 10)
	dir := t.TempDir()
	file := filepath.Join(dir, "chat1.jsonl")
	lines := `{"kind":0,"v":{"customTitle":"Fix tests","requests":[]}}` + "\n" +
		`{"kind":2,"k":["requests"],"v":[{"timestamp":` + ts + `,"modelId":"copilot/gpt-5","message":{"text":"run it"},"modelState":{"value":0},"response":[]}]}` + "\n" +
		`{"kind":2,"k":["requests",0,"response"],"v":[{"kind":"toolInvocationSerialized","toolCallId":"c1","toolId":"run_in_terminal","toolSpecificData":{"kind":"terminal","commandLine":{"original":"git push"}}}]}` + "\n"
	if err := os.WriteFile(file, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	read := func(e *vscodeEntry) *Session {
		st, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.readLog(file, st.Size(), st.ModTime().UnixMilli(), fileIno(st)); err != nil {
			t.Fatal(err)
		}
		e.trackUsage(now)
		e.trackTools(now)
		return deriveVSCodeChat(file, e, "Code", st.ModTime().UnixMilli(), now)
	}
	e := &vscodeEntry{folder: "/src/proj"}
	s := read(e)
	if !s.Waiting || s.Busy || len(s.RunningTools) != 1 || s.RunningTools[0].Sink != "Git" || s.Model != "gpt-5" || s.Repo != "proj" || s.ID != "chat1" {
		t.Fatalf("waiting session = %+v", s)
	}

	// Confirm the tool: the session is busy, not waiting.
	f, err := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"kind":1,"k":["requests",0,"response",0,"isConfirmed"],"v":{"type":1}}` + "\n")
	_ = f.Close()
	if s = read(e); s.Waiting || !s.Busy {
		t.Fatalf("busy session = %+v", s)
	}

	// Complete the request: idle, with a tool flow and model tokens.
	f, _ = os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString(`{"kind":1,"k":["requests",0,"response",0,"isComplete"],"v":true}` + "\n" +
		`{"kind":1,"k":["requests",0,"modelState"],"v":{"value":1,"completedAt":` + strconv.FormatInt(now-1000, 10) + `}}` + "\n" +
		`{"kind":1,"k":["requests",0,"promptTokens"],"v":100}` + "\n" +
		`{"kind":1,"k":["requests",0,"completionTokens"],"v":20}` + "\n")
	_ = f.Close()
	s = read(e)
	if s.Waiting || s.Busy || len(s.RunningTools) != 0 || s.Tokens.In != 100 || s.ModelTokens["gpt-5"] == nil {
		t.Fatalf("idle session = %+v", s)
	}
	sinks := map[string]bool{}
	for _, fl := range s.Flows {
		sinks[fl.Sink] = true
	}
	if !sinks["Git"] || !sinks["LLM: gpt-5"] {
		t.Fatalf("flows = %+v", s.Flows)
	}
}

func TestAskUserStaysWaiting(t *testing.T) {
	root := t.TempDir()
	id := "00000000-0000-0000-0000-000000000001"
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339Nano)
	evs := `{"type":"user.message","timestamp":"` + old + `","data":{"content":"hi"}}` + "\n" +
		`{"type":"tool.execution_start","timestamp":"` + old + `","data":{"toolCallId":"q1","toolName":"ask_user","arguments":{"question":"ok?"}}}` + "\n" +
		`{"type":"tool.execution_start","timestamp":"` + old + `","data":{"toolCallId":"b1","toolName":"bash","arguments":{"command":"ls"}}}` + "\n"
	_ = os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(evs), 0o600)
	writeLock(t, dir, time.Now().Add(-3*time.Hour))
	c := &Collector{stateDir: root, sessions: map[string]*sessionEntry{}, vscode: map[string]*vscodeEntry{}}
	s := c.collectCopilotSession(id, time.Now().UnixMilli())
	if s == nil || !s.Waiting || len(s.RunningTools) != 1 || s.RunningTools[0].Name != "ask_user" {
		t.Fatalf("session = %+v", s)
	}
}

func TestInFolder(t *testing.T) {
	cases := []struct {
		cwd, folder string
		want        bool
	}{
		{`C:\work\repo\sub`, `C:\work\repo`, true},
		{`c:/work/repo`, `C:\Work\Repo\`, true},
		{"/a/b", "/a/b/", true},
		{"/a/bc", "/a/b", false},
		{"", "/a", false},
	}
	for _, c := range cases {
		if got := InFolder(c.cwd, c.folder); got != c.want {
			t.Errorf("InFolder(%q, %q) = %v", c.cwd, c.folder, got)
		}
	}
}

func TestLegacyJSONSameSize(t *testing.T) {
	file := filepath.Join(t.TempDir(), "old.json")
	ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
	mk := func(v int) []byte {
		return []byte(`{"customTitle":"legacy","requests":[{"timestamp":` + ts + `,"message":{"text":"x"},"modelState":{"value":` + strconv.Itoa(v) + `},"response":[]}]}`)
	}
	e := &vscodeEntry{}
	read := func() *Session {
		st, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.readLog(file, st.Size(), st.ModTime().UnixMilli(), fileIno(st)); err != nil {
			t.Fatal(err)
		}
		now := time.Now().UnixMilli()
		e.trackTools(now)
		return deriveVSCodeChat(file, e, "Code", st.ModTime().UnixMilli(), now)
	}
	if err := os.WriteFile(file, mk(0), 0o600); err != nil {
		t.Fatal(err)
	}
	if !read().Busy {
		t.Fatal("pending request must be busy")
	}
	later := time.Now().Add(time.Second)
	if err := os.WriteFile(file, mk(1), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(file, later, later); err != nil {
		t.Fatal(err)
	}
	if read().Busy {
		t.Fatal("completed request with the same size must not be busy")
	}
}

func TestTerminalCommandEdited(t *testing.T) {
	cases := []struct{ cl, want string }{
		{`{"original":"ls","toolEdited":"git status","userEdited":"gh pr list"}`, "gh pr list"},
		{`{"original":"ls","toolEdited":"git status"}`, "git status"},
		{`{"original":"ls","forDisplay":"curl x"}`, "ls"},
		{`{"forDisplay":"curl x"}`, "curl x"},
	}
	for _, c := range cases {
		var m map[string]any
		if err := json.Unmarshal([]byte(`{"toolId":"run_in_terminal","toolSpecificData":{"kind":"terminal","commandLine":`+c.cl+`}}`), &m); err != nil {
			t.Fatal(err)
		}
		if got := terminalCommand(m); got != c.want {
			t.Errorf("terminalCommand(%s) = %q, want %q", c.cl, got, c.want)
		}
	}
}

func TestVSCodeToolDoneAndResultError(t *testing.T) {
	now := time.Now().UnixMilli()
	ts := strconv.FormatInt(now-5000, 10)
	state := `{"requests":[{"timestamp":` + ts + `,"modelState":{"value":1,"completedAt":` + strconv.FormatInt(now-1000, 10) + `},"response":[` +
		`{"kind":"toolInvocationSerialized","toolCallId":"a","toolId":"copilot_readFile","isComplete":true},` +
		`{"kind":"toolInvocationSerialized","toolCallId":"b","toolId":"copilot_memory","isComplete":true,"resultError":"boom"},` +
		`{"kind":"toolInvocationSerialized","toolCallId":"c","toolId":"copilot_fetchWebPage","isComplete":true,"summary":{"kind":"incomplete"}}]}]}`
	e := &vscodeEntry{tools: map[string]*vscodeToolRec{}}
	if err := json.Unmarshal([]byte(state), &e.state); err != nil {
		t.Fatal(err)
	}
	e.trackTools(now)
	if e.tools["c"].endAt != 0 {
		t.Fatalf("incomplete tool has an end time: %+v", e.tools["c"])
	}
	s := deriveVSCodeChat("chat.jsonl", e, "Code", now, now)
	ok := map[string]bool{}
	for _, f := range s.Flows {
		ok[f.Sink] = f.OK
	}
	if len(s.Flows) != 2 || !ok["Filesystem"] || ok["Memory (local)"] {
		t.Fatalf("flows = %+v", s.Flows)
	}
}

func TestWorkspaceRootsCodeWorkspace(t *testing.T) {
	dir := t.TempDir()
	wsFile := filepath.Join(dir, "team", "multi.code-workspace")
	if err := os.MkdirAll(filepath.Dir(wsFile), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := "\uFEFF{\n  // the roots\n  \"folders\": [\n    { \"path\": \"../api\" }, /* backend */\n    { \"path\": \"web\", \"name\": \"a // b, ]\" },\n  ],\n  \"settings\": {},\n}\n"
	if err := os.WriteFile(wsFile, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	wsDir := filepath.Join(dir, "storage")
	_ = os.MkdirAll(wsDir, 0o700)
	u := (&url.URL{Scheme: "file", Path: filepath.ToSlash(wsFile)}).String()
	if runtime.GOOS == "windows" {
		u = "file:///" + filepath.ToSlash(wsFile)
	}
	b, _ := json.Marshal(map[string]string{"workspace": u})
	if err := os.WriteFile(filepath.Join(wsDir, "workspace.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	got, saved := workspaceRoots(wsDir)
	want := []string{filepath.Join(dir, "api"), filepath.Join(dir, "team", "web")}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] || saved != wsFile {
		t.Fatalf("workspaceRoots = %q, %q, want %q, %q", got, saved, want, wsFile)
	}
	c := &Collector{vscodeDirs: []string{dir}}
	_ = os.MkdirAll(filepath.Join(dir, "workspaceStorage"), 0o700)
	if f := c.savedWorkspace([]string{want[1], want[0]}); f != "" {
		t.Fatalf("savedWorkspace without a storage entry = %q", f)
	}
	if err := os.Rename(wsDir, filepath.Join(dir, "workspaceStorage", "abc")); err != nil {
		t.Fatal(err)
	}
	// The new storage entry changes the workspaceStorage folder, so the empty result is not used again.
	_ = os.Chtimes(filepath.Join(dir, "workspaceStorage"), time.Now().Add(time.Minute), time.Now().Add(time.Minute))
	if f := c.savedWorkspace([]string{want[1], want[0]}); f != wsFile {
		t.Fatalf("savedWorkspace = %q, want %q", f, wsFile)
	}
	if f := c.savedWorkspace([]string{want[0]}); f != "" {
		t.Fatalf("savedWorkspace of other roots = %q", f)
	}
	untitled := filepath.Join(dir, "Workspaces", "1", "workspace.json")
	_ = os.MkdirAll(filepath.Dir(untitled), 0o700)
	if err := os.WriteFile(untitled, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	uu := (&url.URL{Scheme: "file", Path: filepath.ToSlash(untitled)}).String()
	if runtime.GOOS == "windows" {
		uu = "file:///" + filepath.ToSlash(untitled)
	}
	b, _ = json.Marshal(map[string]string{"workspace": uu})
	_ = os.MkdirAll(filepath.Join(dir, "untitled"), 0o700)
	_ = os.WriteFile(filepath.Join(dir, "untitled", "workspace.json"), b, 0o600)
	if roots, saved := workspaceRoots(filepath.Join(dir, "untitled")); len(roots) != 2 || saved != "" {
		t.Fatalf("untitled workspace: %q, %q", roots, saved)
	}
	s := &Session{Cwd: got[0], Folders: got}
	if r := sessionRoots(s); len(r) != 2 {
		t.Fatalf("sessionRoots = %q", r)
	}
	snap := &Snapshot{Sessions: []*Session{{Busy: true, Cwd: got[0], Folders: got}}, IDEs: []IDE{{IDE: "Code", Folders: []string{want[1]}}}}
	if sum := Summarize(snap, true); len(sum.VSCodeWindows) != 1 {
		t.Fatalf("window of the second root not in use: %+v", sum)
	}
}

func TestTurnEndInputAndLLMWindow(t *testing.T) {
	p := newParseState()
	at := func(d time.Duration) string { return time.Now().Add(d).UTC().Format(time.RFC3339Nano) }
	p.apply(ev(t, `{"type":"assistant.turn_start","timestamp":"`+at(-time.Hour)+`","data":{}}`))
	p.apply(ev(t, `{"type":"session.usage_record","timestamp":"`+at(-time.Hour)+`","data":{"usage":{"model":"m","inputTokens":1}}}`))
	p.apply(ev(t, `{"type":"elicitation.requested","timestamp":"`+at(0)+`","data":{}}`))
	p.apply(ev(t, `{"type":"user_input.requested","timestamp":"`+at(0)+`","data":{}}`))
	p.apply(ev(t, `{"type":"user_input.completed","timestamp":"`+at(0)+`","data":{}}`))
	p.apply(ev(t, `{"type":"assistant.turn_end","timestamp":"`+at(0)+`","data":{}}`))
	if p.turnOpen || len(p.inputs.keys) != 1 || len(p.llm) != 0 {
		t.Fatalf("turnOpen=%v inputs=%v llm=%d", p.turnOpen, p.inputs.keys, len(p.llm))
	}
	for i := 0; i < maxFlows+10; i++ {
		p.apply(ev(t, `{"type":"session.usage_record","timestamp":"`+at(0)+`","data":{"usage":{"model":"m","inputTokens":1}}}`))
	}
	p.apply(ev(t, `{"type":"permission.requested","timestamp":"`+at(0)+`","data":{}}`))
	p.apply(ev(t, `{"type":"abort","timestamp":"`+at(0)+`","data":{}}`))
	if len(p.inputs.keys) != 0 || len(p.perms.keys) != 0 || len(p.llm) != maxFlows+10 {
		t.Fatalf("inputs=%v perms=%v llm=%d", p.inputs.keys, p.perms.keys, len(p.llm))
	}
}

func TestMainModelFromEvents(t *testing.T) {
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	p := newParseState()
	p.apply(ev(t, `{"type":"session.start","timestamp":"`+ts+`","data":{"selectedModel":"gpt-5"}}`))
	if p.model != "gpt-5" {
		t.Fatalf("start model = %q", p.model)
	}
	p.apply(ev(t, `{"type":"session.model_change","timestamp":"`+ts+`","data":{"newModel":"claude-sonnet-5"}}`))
	if p.model != "claude-sonnet-5" {
		t.Fatalf("model_change model = %q", p.model)
	}
	// A completion whose start is not known still updates the model.
	p.apply(ev(t, `{"type":"tool.execution_complete","timestamp":"`+ts+`","data":{"toolCallId":"x","model":"gpt-6"}}`))
	if p.model != "gpt-6" {
		t.Fatalf("completion model = %q", p.model)
	}
	// Sub-agent events never change the main model.
	p.apply(ev(t, `{"type":"session.model_change","agentId":"a1","timestamp":"`+ts+`","data":{"newModel":"sub1"}}`))
	p.apply(ev(t, `{"type":"tool.execution_complete","timestamp":"`+ts+`","data":{"parentToolCallId":"c1","toolCallId":"y","model":"sub2"}}`))
	p.apply(ev(t, `{"type":"session.resume","agentId":"a1","timestamp":"`+ts+`","data":{"selectedModel":"sub3"}}`))
	if p.model != "gpt-6" {
		t.Fatalf("sub-agent changed model to %q", p.model)
	}
	p.apply(ev(t, `{"type":"session.resume","timestamp":"`+ts+`","data":{"selectedModel":"gpt-5"}}`))
	if p.model != "gpt-5" {
		t.Fatalf("resume model = %q", p.model)
	}
	for _, typ := range []string{"session.start", "session.resume", "session.model_change"} {
		if !relevantTypes[typ] {
			t.Errorf("%s is not relevant", typ)
		}
	}
}

func TestPrescanModelAndTailBoundary(t *testing.T) {
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	var b strings.Builder
	b.WriteString(`{"type":"session.start","timestamp":"` + ts + `","data":{"selectedModel":"gpt-5"}}` + "\n")
	b.WriteString(`{"type":"session.model_change","timestamp":"` + ts + `","data":{"newModel":"claude-sonnet-5"}}` + "\n")
	cross := int64(b.Len())
	b.WriteString(`{"type":"user.message","timestamp":"` + ts + `","data":{"content":"across the boundary ` + strings.Repeat("y", 900) + `"}}` + "\n")
	b.WriteString(`{"type":"other","data":"` + strings.Repeat("z", initialTail-500) + `"}` + "\n")
	dir := t.TempDir()
	f := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(f, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(f)
	if err != nil {
		t.Fatal(err)
	}
	if start := st.Size() - initialTail; start <= cross || start >= cross+900 {
		t.Fatalf("the tail start %d is not inside the user message at %d", start, cross)
	}
	e := &sessionEntry{}
	if err := e.readAppended(f, st.Size(), fileIno(st)); err != nil {
		t.Fatal(err)
	}
	if e.p.model != "claude-sonnet-5" {
		t.Fatalf("model = %q", e.p.model)
	}
	if !strings.HasPrefix(e.p.lastUser, "across the boundary") {
		t.Fatalf("line across the tail boundary was lost: %q", e.p.lastUser)
	}
}

func TestSubagentRunningOnlyWhenLive(t *testing.T) {
	root := t.TempDir()
	id := "00000000-0000-0000-0000-000000000002"
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	evs := `{"type":"subagent.started","timestamp":"` + ts + `","data":{"toolCallId":"c1","agentDisplayName":"Explore"}}` + "\n"
	_ = os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(evs), 0o600)
	c := &Collector{stateDir: root, sessions: map[string]*sessionEntry{}, vscode: map[string]*vscodeEntry{}}
	s := c.collectCopilotSession(id, time.Now().UnixMilli())
	if s == nil || s.Live || len(s.Subagents) != 1 || s.Subagents[0].Running || s.Busy {
		t.Fatalf("dead session = %+v", s)
	}
	_ = os.WriteFile(filepath.Join(dir, "inuse."+strconv.Itoa(os.Getpid())+".lock"), nil, 0o600)
	s = c.collectCopilotSession(id, time.Now().UnixMilli())
	if s == nil || !s.Live || len(s.Subagents) != 1 || !s.Subagents[0].Running || !s.Busy {
		t.Fatalf("live session = %+v", s)
	}
}

func TestVSCodeModelTotals(t *testing.T) {
	now := time.Now().UnixMilli()
	end := strconv.FormatInt(now-1000, 10)
	state := replay(t, `{"kind":0,"v":{"requests":[`+
		`{"timestamp":`+end+`,"modelId":"copilot/auto","promptTokens":999,"completionTokens":999,"modelState":{"value":1,"completedAt":`+end+`},`+
		`"modelTotals":[{"model":"copilot/gpt-5","inputTokens":100,"outputTokens":10},{"model":"claude-sonnet-5","inputTokens":50,"outputTokens":5},`+
		`{"model":"bad","inputTokens":-1,"outputTokens":5},{"model":"","inputTokens":1,"outputTokens":1},{"model":"str","inputTokens":"1","outputTokens":1}]},`+
		`{"timestamp":`+end+`,"modelId":"copilot/gpt-5","promptTokens":7,"completionTokens":3,"modelState":{"value":1,"completedAt":`+end+`},"modelTotals":[{"model":"x"}]}`+
		`]}}`)
	e := &vscodeEntry{state: state}
	e.trackUsage(now)
	s := deriveVSCodeChat("c.jsonl", e, "Code", now, now)
	g, c := s.ModelTokens["gpt-5"], s.ModelTokens["claude-sonnet-5"]
	if g == nil || g.In != 107 || g.Out != 13 || !g.CallsUnknown || c == nil || c.In != 50 || c.Out != 5 || len(s.ModelTokens) != 2 {
		t.Fatalf("modelTokens = %+v", s.ModelTokens)
	}
	if s.Tokens.In != 157 || s.Tokens.Out != 18 {
		t.Fatalf("tokens = %+v", s.Tokens)
	}
	llm := 0
	for _, f := range s.Flows {
		if f.Tool == "model" {
			llm++
		}
	}
	// Totals that are in the file at the first read are old use.
	if llm != 0 {
		t.Fatalf("flows = %+v", s.Flows)
	}
}

func TestVSCodeChatDedupe(t *testing.T) {
	userDir := filepath.Join(t.TempDir(), "Code", "User")
	write := func(ws, name, title string, mtime time.Time) {
		t.Helper()
		dir := filepath.Join(userDir, "workspaceStorage", ws, "chatSessions")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		body := `{"customTitle":"` + title + `","requests":[]}`
		if strings.HasSuffix(name, ".jsonl") {
			body = `{"kind":0,"v":` + body + `}` + "\n"
		}
		f := filepath.Join(dir, name)
		if err := os.WriteFile(f, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(f, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Now().Add(-time.Minute).Truncate(time.Second)
	// Newest file wins.
	write("ws1", "s1.jsonl", "old", base)
	write("ws2", "s1.jsonl", "new", base.Add(time.Second))
	// Same time: .jsonl wins over .json.
	write("ws1", "s2.json", "json", base)
	write("ws2", "s2.jsonl", "jsonl", base)
	// Same time and format: first path wins.
	write("ws1", "s3.jsonl", "first", base)
	write("ws2", "s3.jsonl", "second", base)
	c := &Collector{vscodeDirs: []string{userDir}, sessions: map[string]*sessionEntry{}, vscode: map[string]*vscodeEntry{}}
	got := map[string]string{}
	for _, s := range c.collectVSCodeChats(time.Now().UnixMilli()) {
		if _, dup := got[s.ID]; dup {
			t.Fatalf("duplicate session %s", s.ID)
		}
		got[s.ID] = s.Name
	}
	want := map[string]string{"s1": "new", "s2": "jsonl", "s3": "first"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if len(got) != 3 || len(c.vscode) != 3 {
		t.Fatalf("sessions = %v, cache = %d", got, len(c.vscode))
	}
}

// writeLock makes a lock file of this process with the time at.

func writeLock(t *testing.T, dir string, at time.Time) {
	t.Helper()
	lock := filepath.Join(dir, "inuse."+strconv.Itoa(os.Getpid())+".lock")
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(lock, at, at); err != nil {
		t.Fatal(err)
	}
	if err := setBirth(lock, at); err != nil {
		t.Fatal(err)
	}
}

func TestVSCodeAppData(t *testing.T) {
	home := filepath.FromSlash("/home/u")
	abs := filepath.FromSlash("/data/vs")
	if runtime.GOOS == "windows" {
		abs = `C:\data\vs`
	}
	cases := []struct {
		env        map[string]string
		goos, want string
	}{
		{map[string]string{"VSCODE_APPDATA": abs, "APPDATA": abs + "x"}, "windows", abs},
		{map[string]string{"VSCODE_APPDATA": "rel", "APPDATA": abs}, "windows", abs},
		{map[string]string{"APPDATA": "rel"}, "windows", filepath.Join(home, "AppData", "Roaming")},
		{map[string]string{}, "windows", filepath.Join(home, "AppData", "Roaming")},
		{map[string]string{"XDG_CONFIG_HOME": abs}, "darwin", filepath.Join(home, "Library", "Application Support")},
		{map[string]string{"XDG_CONFIG_HOME": abs}, "linux", abs},
		{map[string]string{"XDG_CONFIG_HOME": "rel"}, "linux", filepath.Join(home, ".config")},
		{map[string]string{"VSCODE_APPDATA": abs}, "linux", abs},
	}
	for _, c := range cases {
		getenv := func(k string) string { return c.env[k] }
		if got := vscodeAppData(getenv, c.goos, home); got != c.want {
			t.Errorf("vscodeAppData(%v, %s) = %q, want %q", c.env, c.goos, got, c.want)
		}
	}
}

func TestCommandsVerbatim(t *testing.T) {
	cmd := "cd /src &&  make  test\n  echo done\t" + strings.Repeat("x", 1000)
	if got := describeArgs(map[string]any{"command": cmd}); got != "$ "+cmd {
		t.Errorf("describeArgs = %q", got)
	}
	tool := map[string]any{"toolSpecificData": map[string]any{"kind": "terminal", "commandLine": map[string]any{"original": cmd}}}
	if got := describeVSCodeTool(tool); got != "$ "+cmd {
		t.Errorf("describeVSCodeTool = %q", got)
	}
}

func TestResumeClearsPending(t *testing.T) {
	at := time.Now().UTC().Format(time.RFC3339Nano)
	open := func(p *parseState) {
		p.apply(ev(t, `{"type":"tool.execution_start","timestamp":"`+at+`","data":{"toolCallId":"b1","toolName":"bash","arguments":{"command":"ls"}}}`))
		p.apply(ev(t, `{"type":"permission.requested","timestamp":"`+at+`","data":{"requestId":"r1"}}`))
		p.apply(ev(t, `{"type":"user_input.requested","timestamp":"`+at+`","data":{"requestId":"q1"}}`))
	}
	p := newParseState()
	open(p)
	p.apply(ev(t, `{"type":"session.resume","timestamp":"`+at+`","data":{"alreadyInUse":true}}`))
	if len(p.toolStarts) != 1 || len(p.perms.keys) != 1 || len(p.inputs.keys) != 1 {
		t.Fatalf("alreadyInUse:true must keep the pending work: %+v", p)
	}
	p.apply(ev(t, `{"type":"session.resume","timestamp":"`+at+`","data":{}}`))
	if len(p.toolStarts) != 1 || len(p.perms.keys) != 1 {
		t.Fatal("a resume without alreadyInUse must keep the pending work")
	}
	p.apply(ev(t, `{"type":"session.resume","timestamp":"`+at+`","data":{"alreadyInUse":false}}`))
	if len(p.toolStarts) != 0 || len(p.perms.keys) != 0 || len(p.inputs.keys) != 0 {
		t.Fatalf("alreadyInUse:false must clear the pending work: %+v", p)
	}

	// A completion with an ID removes that request. Without an ID, it removes the oldest one.
	p.apply(ev(t, `{"type":"permission.requested","timestamp":"`+at+`","data":{"requestId":"a"}}`))
	p.apply(ev(t, `{"type":"permission.requested","timestamp":"`+at+`","data":{}}`))
	p.apply(ev(t, `{"type":"permission.requested","timestamp":"`+at+`","data":{"requestId":"c"}}`))
	p.apply(ev(t, `{"type":"permission.completed","timestamp":"`+at+`","data":{"requestId":"zz"}}`))
	p.apply(ev(t, `{"type":"permission.completed","timestamp":"`+at+`","data":{"requestId":"c"}}`))
	p.apply(ev(t, `{"type":"permission.completed","timestamp":"`+at+`","data":{}}`))
	if len(p.perms.keys) != 1 || p.perms.keys[0] != "#0" {
		t.Fatalf("perms = %v", p.perms.keys)
	}
}

func TestPendingWorkBeforeLockIgnored(t *testing.T) {
	root := t.TempDir()
	id := "00000000-0000-0000-0000-000000000003"
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339Nano)
	evs := `{"type":"subagent.started","timestamp":"` + old + `","data":{"toolCallId":"s1","agentDisplayName":"Explore"}}` + "\n" +
		`{"type":"tool.execution_start","timestamp":"` + old + `","data":{"toolCallId":"b1","toolName":"bash","arguments":{"command":"ls"}}}` + "\n" +
		`{"type":"permission.requested","timestamp":"` + old + `","data":{"requestId":"r1"}}` + "\n" +
		`{"type":"elicitation.requested","timestamp":"` + old + `","data":{"requestId":"e1"}}` + "\n"
	_ = os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(evs), 0o600)
	c := &Collector{stateDir: root, sessions: map[string]*sessionEntry{}, vscode: map[string]*vscodeEntry{}}

	// The live process opened the session after the events: the work belongs to a stopped process.
	writeLock(t, dir, time.Now())
	s := c.collectCopilotSession(id, time.Now().UnixMilli())
	if s == nil || !s.Live || s.Waiting || s.Busy || len(s.RunningTools) != 0 || len(s.Subagents) != 0 {
		t.Fatalf("session after a new lock = %+v", s)
	}

	// The live process opened the session before the events: the work is current.
	writeLock(t, dir, time.Now().Add(-time.Hour))
	s = c.collectCopilotSession(id, time.Now().UnixMilli())
	if s == nil || !s.Waiting || len(s.RunningTools) != 1 || len(s.Subagents) != 1 || !s.Subagents[0].Running {
		t.Fatalf("session after an old lock = %+v", s)
	}
}

func TestVSCodeRewriteSameInode(t *testing.T) {
	file := filepath.Join(t.TempDir(), "chat.jsonl")
	pad := strings.Repeat("p", 5000)
	e := &vscodeEntry{}
	read := func(content string, at time.Time) string {
		t.Helper()
		f, err := os.OpenFile(file, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.WriteString(content)
		_ = f.Close()
		_ = os.Chtimes(file, at, at)
		st, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.readLog(file, st.Size(), st.ModTime().UnixMilli(), fileIno(st)); err != nil {
			t.Fatal(err)
		}
		return str(get(e.state, "customTitle"))
	}
	t0 := time.Now().Add(-time.Minute)
	first := `{"kind":0,"v":{"pad":"` + pad + `","customTitle":"a","requests":[]}}` + "\n"
	if got := read(first, t0); got != "a" {
		t.Fatalf("title = %q", got)
	}
	// An append is read from the old position.
	if got := read(first+`{"kind":1,"k":["customTitle"],"v":"b"}`+"\n", t0.Add(time.Second)); got != "b" {
		t.Fatalf("title after append = %q", got)
	}
	// The same size, but the bytes before the read position changed.
	if got := read(first+`{"kind":1,"k":["customTitle"],"v":"c"}`+"\n", t0.Add(2*time.Second)); got != "c" {
		t.Fatalf("title after a rewrite of the tail = %q", got)
	}
	// One new full-state line that is longer than the old log.
	full := `{"kind":0,"v":{"customTitle":"d","pad":"` + pad + pad + `","requests":[]}}` + "\n"
	if got := read(full, t0.Add(3*time.Second)); got != "d" {
		t.Fatalf("title after a full rewrite = %q", got)
	}
}

func TestVSCodeTokenDeltas(t *testing.T) {
	now := time.Now().UnixMilli()
	req := func(tin, tout int) any {
		return replay(t, `{"kind":0,"v":{"requests":[{"requestId":"r1","timestamp":`+strconv.FormatInt(now-60000, 10)+
			`,"modelId":"copilot/gpt-5","modelState":{"value":0},"promptTokens":`+strconv.Itoa(tin)+`,"completionTokens":`+strconv.Itoa(tout)+`}]}}`)
	}
	llm := func(s *Session) (n int, tin, tout int64) {
		for _, f := range s.Flows {
			if f.Tool == "model" {
				n++
				tin += f.Tin
				tout += f.Tout
			}
		}
		return
	}
	e := &vscodeEntry{state: req(100, 10)}
	poll := func(at int64) *Session {
		e.trackUsage(at)
		e.trackTools(at)
		return deriveVSCodeChat("c.jsonl", e, "Code", at, at)
	}
	if n, _, _ := llm(poll(now)); n != 0 {
		t.Fatal("totals at the first read must not be recent")
	}
	e.state = req(150, 12)
	s := poll(now + 2000)
	if n, tin, tout := llm(s); n != 1 || tin != 50 || tout != 2 {
		t.Fatalf("delta = %d %d %d", n, tin, tout)
	}
	if s.Tokens.In != 150 || s.Tokens.Out != 12 {
		t.Fatalf("tokens = %+v", s.Tokens)
	}
	// VS Code writes the log again with the same totals: no new model call.
	e.reset(0)
	e.state = req(150, 12)
	s = poll(now + 4000)
	if n, tin, _ := llm(s); n != 1 || tin != 50 || s.Tokens.In != 150 {
		t.Fatalf("after rewrite: calls=%d tin=%d tokens=%+v", n, tin, s.Tokens)
	}
	// The model calls leave the window.
	if n, _, _ := llm(poll(now + 4000 + flowWindowMS)); n != 0 {
		t.Fatal("old model calls must leave the window")
	}
}

func TestVSCodeSubagentModel(t *testing.T) {
	now := time.Now().UnixMilli()
	ts := strconv.FormatInt(now-1000, 10)
	state := replay(t, `{"kind":0,"v":{"requests":[{"timestamp":`+ts+`,"modelState":{"value":0},"response":[`+
		`{"kind":"toolInvocationSerialized","toolCallId":"a","toolSpecificData":{"kind":"subagent","agentName":"explore","agentDisplayName":"Explore","modelId":"copilot/gpt-5"}},`+
		`{"kind":"toolInvocationSerialized","toolCallId":"b","toolSpecificData":{"kind":"subagent","agentName":"task","modelName":"claude-sonnet-5"}},`+
		`{"kind":"toolInvocationSerialized","toolCallId":"c","toolSpecificData":{"kind":"subagent"}}]}]}}`)
	s := deriveVSCodeChat("c.jsonl", &vscodeEntry{state: state, tools: map[string]*vscodeToolRec{}}, "Code", now, now)
	want := []Subagent{{Name: "Explore", Type: "explore", Model: "gpt-5"}, {Name: "task", Type: "task", Model: "claude-sonnet-5"}, {Name: "subagent", Type: "subagent"}}
	if len(s.Subagents) != len(want) {
		t.Fatalf("subagents = %+v", s.Subagents)
	}
	for i, w := range want {
		if a := s.Subagents[i]; a.Name != w.Name || a.Type != w.Type || a.Model != w.Model {
			t.Errorf("subagent %d = %+v, want %+v", i, a, w)
		}
	}
}

func TestToolRejected(t *testing.T) {
	cases := []struct {
		confirmed any
		want      bool
	}{
		{false, true}, {true, false}, {nil, false},
		{map[string]any{"type": float64(0)}, true}, {map[string]any{"type": float64(5)}, true},
		{map[string]any{"type": float64(1)}, false}, {map[string]any{}, false},
	}
	for _, c := range cases {
		if got := toolRejected(map[string]any{"isConfirmed": c.confirmed}); got != c.want {
			t.Errorf("toolRejected(%v) = %v, want %v", c.confirmed, got, c.want)
		}
	}
}

func TestMutateRejectsBadKeys(t *testing.T) {
	kind := 1
	for _, k := range []string{`["__proto__","x"]`, `["a","constructor"]`, `["prototype"]`, `["a",true]`, `["a",null]`, `[{"x":1}]`} {
		var key any
		if err := json.Unmarshal([]byte(k), &key); err != nil {
			t.Fatal(err)
		}
		root := map[string]any{"a": map[string]any{}}
		got := mutate(root, &vscodeMutation{Kind: &kind, K: key, V: 1.0})
		if b, _ := json.Marshal(got); string(b) != `{"a":{}}` {
			t.Fatalf("%s: state = %s", k, b)
		}
	}
	root := mutate(map[string]any{}, &vscodeMutation{Kind: &kind, K: []any{"a", 0.0}, V: "x"})
	if b, _ := json.Marshal(root); string(b) != `{"a":["x"]}` {
		t.Fatalf("state = %s", b)
	}
}

func TestCallsJSON(t *testing.T) {
	cases := []struct {
		v    any
		want string
	}{
		{Flow{Tool: "model", Tin: 1, Observation: true, CallsUnknown: true}, `"calls":null`},
		{ModelUsage{In: 1, Out: 2, Calls: 3, CallsUnknown: true}, `{"in":1,"out":2,"calls":null}`},
		{ModelUsage{In: 1, Out: 2, Calls: 3}, `{"in":1,"out":2,"calls":3}`},
		{&ModelUsage{In: 1, CallsUnknown: true}, `"calls":null`},
	}
	for _, c := range cases {
		b, err := json.Marshal(c.v)
		if err != nil || !strings.Contains(string(b), c.want) {
			t.Fatalf("%#v = %s %v, want %s", c.v, b, err, c.want)
		}
	}
	if b, _ := json.Marshal(Flow{Tool: "model"}); strings.Contains(string(b), "calls") {
		t.Fatalf("copilot flow = %s", b)
	}
}

func TestUsageCountsNotNumbers(t *testing.T) {
	p := newParseState()
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	p.apply(ev(t, `{"type":"session.usage_record","timestamp":"`+ts+`","data":{"usage":{"model":"m","inputTokens":"5","outputTokens":-3,"duration":{"x":1}}}}`))
	p.apply(ev(t, `{"type":"session.usage_record","timestamp":"`+ts+`","data":{"usage":{"model":"m","inputTokens":7.9,"outputTokens":2}}}`))
	mt := p.modelTokens["m"]
	if mt == nil || mt.In != 7 || mt.Out != 2 || mt.Calls != 2 || p.llm[0].At != parseTS(ts) {
		t.Fatalf("modelTokens = %#v llm = %#v", mt, p.llm)
	}
}

func TestResumeAbandonStopsSubagents(t *testing.T) {
	p := newParseState()
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	p.apply(ev(t, `{"type":"subagent.started","timestamp":"`+ts+`","data":{"toolCallId":"c1","agentName":"explore"}}`))
	p.apply(ev(t, `{"type":"session.resume","timestamp":"`+ts+`","data":{"alreadyInUse":false}}`))
	if a := p.subs["c1"]; a == nil || a.Running || a.EndedAt != parseTS(ts) {
		t.Fatalf("sub = %#v", a)
	}
}

func TestAbortStopsSubagents(t *testing.T) {
	for _, typ := range []string{"abort", "session.idle", "session.shutdown"} {
		p := newParseState()
		ts := time.Now().UTC().Format(time.RFC3339Nano)
		p.apply(ev(t, `{"type":"subagent.started","timestamp":"`+ts+`","data":{"toolCallId":"c1","agentName":"explore"}}`))
		p.apply(ev(t, `{"type":"`+typ+`","timestamp":"`+ts+`","data":{}}`))
		if a := p.subs["c1"]; a == nil || a.Running || a.EndedAt != parseTS(ts) {
			t.Fatalf("%s: sub = %#v", typ, a)
		}
	}
	p := newParseState()
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	p.apply(ev(t, `{"type":"subagent.started","timestamp":"`+ts+`","data":{"toolCallId":"c1","agentName":"explore"}}`))
	p.apply(ev(t, `{"type":"session.task_complete","timestamp":"`+ts+`","data":{}}`))
	if a := p.subs["c1"]; a == nil || !a.Running {
		t.Fatalf("task_complete: sub = %#v", a)
	}
}

func TestVSCodeReplayKeepsState(t *testing.T) {
	file := filepath.Join(t.TempDir(), "chat.jsonl")
	e := &vscodeEntry{}
	read := func(content string, at time.Time) string {
		t.Helper()
		if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(file, at, at)
		st, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.readLog(file, st.Size(), st.ModTime().UnixMilli(), fileIno(st)); err != nil {
			t.Fatal(err)
		}
		return str(get(e.state, "customTitle"))
	}
	t0 := time.Now().Add(-time.Minute)
	first := `{"kind":0,"v":{"customTitle":"a","requests":[]}}` + "\n"
	if got := read(first, t0); got != "a" || e.replay {
		t.Fatalf("title = %q replay = %v", got, e.replay)
	}
	// The same size and a new time: a rewrite. A line that is not a full state does not change the state.
	pad := strings.Repeat("z", len(first)-len(`{"kind":1,"k":["customTitle"],"v":""}`+"\n"))
	partial := `{"kind":1,"k":["customTitle"],"v":"` + pad + `"}` + "\n"
	if len(partial) != len(first) {
		t.Fatalf("test setup: %d != %d", len(partial), len(first))
	}
	if got := read(partial, t0.Add(time.Second)); got != "a" || !e.replay {
		t.Fatalf("title during replay = %q replay = %v", got, e.replay)
	}
	// The full-state line ends the replay.
	if got := read(partial+`{"kind":0,"v":{"customTitle":"b","requests":[]}}`+"\n", t0.Add(2*time.Second)); got != "b" || e.replay {
		t.Fatalf("title after replay = %q replay = %v", got, e.replay)
	}
}

func TestLegacyJSONParseErrorKeepsState(t *testing.T) {
	file := filepath.Join(t.TempDir(), "old.json")
	e := &vscodeEntry{}
	read := func(content string, at time.Time) string {
		t.Helper()
		if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(file, at, at)
		st, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.readLog(file, st.Size(), st.ModTime().UnixMilli(), fileIno(st)); err != nil {
			t.Fatal(err)
		}
		return str(get(e.state, "customTitle"))
	}
	t0 := time.Now().Add(-time.Minute)
	if got := read(`{"customTitle":"a"}`, t0); got != "a" || e.replay {
		t.Fatalf("title = %q replay = %v", got, e.replay)
	}
	if got := read(`{"customTitle":"b"`, t0.Add(time.Second)); got != "a" {
		t.Fatalf("title after a parse error = %q", got)
	}
	if got := read(`{"customTitle":"c"}`, t0.Add(2*time.Second)); got != "c" {
		t.Fatalf("title after a good write = %q", got)
	}
}

func TestVSCodeSkipsBadRequests(t *testing.T) {
	now := time.Now().UnixMilli()
	e := &vscodeEntry{state: map[string]any{"requests": []any{nil, "x", 3.0, map[string]any{"modelId": "m", "promptTokens": "9", "completionTokens": 4.0,
		"response": []any{nil, "y", map[string]any{"value": "hi"}}}, nil}}, tools: map[string]*vscodeToolRec{}}
	e.trackUsage(now)
	e.trackTools(now)
	s := deriveVSCodeChat("c.jsonl", e, "Code", now, now)
	if s == nil || s.Tokens.In != 0 || s.Tokens.Out != 4 || s.LastAssistant == "" {
		t.Fatalf("session = %+v", s)
	}
	e = &vscodeEntry{tools: map[string]*vscodeToolRec{}}
	e.trackUsage(now)
	e.trackTools(now)
	if deriveVSCodeChat("c.jsonl", e, "Code", now, now) == nil {
		t.Fatal("nil state")
	}
}

func TestPrescanParsedType(t *testing.T) {
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	lines := `{"timestamp":"` + ts + `","type":"subagent.started","data":{"toolCallId":"c1","agentName":"a"}}` + "\n" +
		`{"type":"subagent\u002estarted","timestamp":"` + ts + `","data":{"toolCallId":"c2","agentName":"b"}}` + "\n" +
		`{"type":"tool.execution_start","timestamp":"` + ts + `","data":{"toolCallId":"t1","toolName":"subagent.x","parentToolCallId":"c1"}}` + "\n" +
		`{"type":"session.model_change","timestamp":"` + ts + `","data":{"newModel":"m2"}}` + "\n"
	f := filepath.Join(t.TempDir(), "events.jsonl")
	if err := os.WriteFile(f, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	p := newParseState()
	if _, err := prescanSubagents(f, p, int64(len(lines))); err != nil {
		t.Fatal(err)
	}
	if p.subs["c1"] == nil || p.subs["c2"] == nil || len(p.toolStarts) != 1 || p.toolStarts["t1"].AgentID != "c1" || p.model != "m2" {
		t.Fatalf("subs = %#v tools = %#v model = %q", p.subs, p.toolStarts, p.model)
	}
}

func TestVSCodeNewChatUsage(t *testing.T) {
	now := time.Now().UnixMilli()
	mk := func(ts int64) *vscodeEntry {
		return &vscodeEntry{state: map[string]any{"requests": []any{map[string]any{"requestId": "r1", "timestamp": float64(ts),
			"modelTotals": []any{map[string]any{"model": "gpt-5", "inputTokens": float64(100), "outputTokens": float64(20)}}}}}}
	}
	old := mk(now - 60000)
	old.trackUsage(now)
	if len(old.llm) != 0 {
		t.Fatalf("old use counted: %d", len(old.llm))
	}
	nw := mk(now - 30000)
	nw.scanStart = now - 120000
	nw.trackUsage(now)
	if len(nw.llm) != 1 || nw.llm[0].Tokens != 120 || nw.llm[0].EndAt != now {
		t.Fatalf("new chat: %+v", nw.llm)
	}
	pre := mk(now - 300000)
	pre.scanStart = now - 120000
	pre.trackUsage(now)
	if len(pre.llm) != 0 {
		t.Fatalf("pre-scan use counted: %d", len(pre.llm))
	}
}

func TestParseYAMLCRLF(t *testing.T) {
	o := parseYAML("summary: Hello\r\ncwd: /x\r\n")
	if o["summary"] != "Hello" || o["cwd"] != "/x" {
		t.Fatalf("got %q", o)
	}
}

func TestSummaryOmitsArguments(t *testing.T) {
	s := &Session{ID: "s1", Busy: true, RunningTools: []Tool{{Name: "bash", Desc: "curl -H 'Authorization: Bearer secret'"}},
		Subagents: []Subagent{{Name: "a", Running: true, Doing: "bash: echo secret"}}}
	b, _ := json.Marshal(Summarize(&Snapshot{Sessions: []*Session{s}}, true))
	if strings.Contains(string(b), "secret") || !strings.Contains(string(b), `"runningTools":["bash"]`) {
		t.Fatalf("summary: %s", b)
	}
}
