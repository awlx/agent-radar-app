package hermes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/awlx/agent-radar-app/internal/collector"
)

const (
	testUser = "user"
	testPass = "s3cret-pass"
)

// mock is a fake Hermes server. With token set it is an API server; else it is a dashboard with a password login.
type mock struct {
	t      *testing.T
	token  string
	now    float64 // Unix seconds
	mu     sync.Mutex
	logins int
	expire bool // the next authorized request gets 401 one time
	paths  []string
	noSort bool                        // the server does not know order=recent
	extra  []map[string]any            // more top-level session rows
	msgs   map[string][]map[string]any // messages of the extra rows
	jobs   []map[string]any            // cron jobs; nil: the server has no cron API
	config map[string]any              // the /api/config document; nil: no config API
	kids   []map[string]any            // delegate child sessions: only search and the detail route find them
}

func (m *mock) authorized(r *http.Request) bool {
	if m.token != "" {
		return r.Header.Get("Authorization") == "Bearer "+m.token
	}
	c, err := r.Cookie("hermes_session_at")
	return err == nil && c.Value == "ok"
}

func (m *mock) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	m.paths = append(m.paths, r.Method+" "+r.URL.String())
	m.mu.Unlock()
	write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	dash := m.token == ""
	switch {
	case r.URL.Path == "/api/status" && dash:
		write(map[string]any{"gateway_state": "running", "auth_providers": []string{"basic"}, "active_agents": 1})
		return
	case r.URL.Path == "/v1/capabilities" && !dash:
		if !m.authorized(r) {
			w.WriteHeader(401)
			return
		}
		write(map[string]any{"object": "capabilities"})
		return
	case r.URL.Path == "/api/auth/providers" && dash:
		write(map[string]any{"providers": []map[string]any{{"name": "basic", "supports_password": true}}})
		return
	case r.URL.Path == "/auth/password-login" && dash && r.Method == http.MethodPost:
		var b map[string]string
		_ = json.NewDecoder(r.Body).Decode(&b)
		if b["username"] != testUser || b["password"] != testPass || b["provider"] != "basic" {
			w.WriteHeader(401)
			return
		}
		m.mu.Lock()
		m.logins++
		m.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "hermes_session_at", Value: "ok", Path: "/"})
		write(map[string]any{"ok": true, "next": "/"})
		return
	}
	if r.URL.Path == "/api/cron/jobs" && dash && m.jobs != nil {
		if !m.authorized(r) {
			w.WriteHeader(401)
			return
		}
		write(m.jobs)
		return
	}
	if r.URL.Path == "/api/config" && dash && m.config != nil {
		if !m.authorized(r) {
			w.WriteHeader(401)
			return
		}
		write(m.config)
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/api/sessions") {
		w.WriteHeader(404)
		return
	}
	m.mu.Lock()
	expire := m.expire
	m.expire = false
	m.mu.Unlock()
	if !m.authorized(r) || expire {
		w.WriteHeader(401)
		return
	}
	if m.noSort && r.URL.Query().Get("order") == "recent" {
		w.WriteHeader(422)
		return
	}
	if dash && r.URL.Query().Get("profile") != "work" {
		m.t.Errorf("dashboard request without profile: %s", r.URL)
	}
	if !dash && r.URL.Query().Get("profile") != "" {
		m.t.Errorf("API server request with profile: %s", r.URL)
	}
	now := m.now
	envelope := func(key string, v any) {
		if dash {
			write(map[string]any{key: v, "total": 3})
		} else {
			write(map[string]any{"object": "list", "data": v, "has_more": false})
		}
	}
	switch r.URL.Path {
	case "/api/sessions/search":
		q := strings.ToLower(strings.Trim(r.URL.Query().Get("q"), `"`))
		var res []map[string]any
		for _, k := range m.kids {
			if r.URL.Query().Get("source") == "subagent" && strings.Contains(strings.ToLower(k["preview"].(string)), q) {
				res = append(res, k)
			}
		}
		write(map[string]any{"results": res})
		return
	case "/api/sessions":
		if r.URL.Query().Get("source") == "subagent" {
			envelope("sessions", []map[string]any{
				{"id": "sub1", "source": "subagent", "parent_session_id": "top1", "model": "hermes-4-70b", "title": "Fetch the baseline",
					"started_at": now - 60, "last_active": now - 5, "ended_at": nil},
				{"id": "sub2", "source": "subagent", "parent_session_id": "top1", "model": "hermes-4-70b", "title": "Old helper",
					"started_at": now - 900, "last_active": now - 800, "ended_at": now - 800},
				{"id": "top1", "source": "cli"},                                  // a server that ignores the filter: this row must not count as a sub-agent
				{"id": "cont1", "source": "matrix", "parent_session_id": "top2"}, // a continued session is not a sub-agent
			})
			return
		}
		envelope("sessions", append([]map[string]any{
			{"id": "top1", "source": "cli", "model": "hermes-4-405b", "title": "Benchmark summary", "cwd": "/home/me/src/bench",
				"started_at": now - 600, "last_active": now - 2, "input_tokens": 1200.0, "output_tokens": 300, "api_call_count": 4,
				"last_activity_at": now - 1, "last_activity_description": "Running the compare script"},
			{"id": "top2", "source": "matrix", "model": "hermes-4-405b", "preview": "Need your input",
				"started_at": int64(now*1000) - 300_000, "last_active": time.UnixMilli(int64(now*1000) - 10_000).UTC().Format(time.RFC3339)},
			{"id": "old", "source": "cli", "title": "Last week", "started_at": now - 8*86400, "last_active": now - 8*86400, "ended_at": now - 8*86400},
			{"id": "sub1", "source": "subagent", "parent_session_id": "top1"},
			{"id": "cont1", "source": "matrix", "parent_session_id": "top2", "title": "Continued chat", "started_at": now - 7200, "last_active": now - 7000, "ended_at": nil},
		}, m.extra...))
	case "/api/sessions/top1/messages":
		// Newest first, to check the sort. tool_calls is a JSON text, as in the Hermes database.
		calls, _ := json.Marshal([]map[string]any{
			{"id": "c1", "type": "function", "function": map[string]any{"name": "read_file", "arguments": `{"path":"results/summary.csv"}`}},
			{"id": "c2", "type": "function", "function": map[string]any{"name": "terminal", "arguments": `{"command":"python compare.py results/"}`}},
		})
		envelope("messages", []map[string]any{
			{"role": "tool", "tool_call_id": "c1", "tool_name": "read_file", "content": "a,b", "timestamp": now - 3},
			{"role": "assistant", "content": []map[string]string{{"type": "text", "text": "I read the results."}}, "tool_calls": string(calls), "timestamp": now - 5},
			{"role": "user", "content": "Compare the benchmark with the baseline.", "timestamp": now - 8},
		})
	case "/api/sessions/top2/messages":
		envelope("messages", []map[string]any{
			{"role": "user", "content": "Deploy it.", "timestamp": now - 30},
			{"role": "assistant", "content": "", "tool_calls": []map[string]any{{"id": "q1", "function": map[string]any{"name": "clarify", "arguments": map[string]any{"question": "Staging or prod?"}}}}, "timestamp": now - 10},
		})
	case "/api/sessions/sub1/messages":
		envelope("messages", []map[string]any{
			{"role": "user", "content": "Fetch the baseline numbers.", "timestamp": now - 60},
			{"role": "assistant", "tool_calls": []map[string]any{{"id": "w1", "function": map[string]any{"name": "web_extract", "arguments": map[string]any{"urls": []string{"https://example.com/baseline"}}}}}, "timestamp": now - 5},
		})
	default:
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/sessions/"), "/messages")
		if v, ok := m.msgs[id]; ok && strings.HasSuffix(r.URL.Path, "/messages") {
			envelope("messages", v)
			return
		}
		for _, k := range m.kids {
			if k["id"] == id {
				write(k)
				return
			}
		}
		w.WriteHeader(404)
	}
}

func startMock(t *testing.T, m *mock) (*httptest.Server, *Config) {
	t.Helper()
	m.t = t
	m.now = float64(time.Now().UnixMilli()) / 1000
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	getenv := func(k string) string {
		switch k {
		case EnvToken:
			return m.token
		case EnvUser:
			if m.token == "" {
				return testUser
			}
		case EnvPassword:
			if m.token == "" {
				return testPass
			}
		}
		return ""
	}
	cfg, err := LoadConfig("", srv.URL+"/sessions?profile=work", getenv)
	if err != nil || cfg == nil {
		t.Fatalf("LoadConfig: %v %v", cfg, err)
	}
	return srv, cfg
}

func TestDashboardMapping(t *testing.T) {
	m := &mock{}
	_, cfg := startMock(t, m)
	src := New(cfg)
	if err := src.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m.logins != 1 {
		t.Fatalf("logins = %d, want 1", m.logins)
	}
	got := src.Sessions(time.Now().UnixMilli())
	if len(got) != 3 {
		t.Fatalf("got %d sessions, want 3 (old and sub-agent rows must not show): %+v", len(got), got)
	}
	byID := map[string]int{}
	for i, s := range got {
		byID[s.ID] = i
		if s.Surface != Surface || s.Kind != "hermes" || s.Remote != cfg.Host() {
			t.Errorf("session %s: surface %q kind %q remote %q", s.ID, s.Surface, s.Kind, s.Remote)
		}
	}
	top := got[byID["hermes:"+cfg.Host()+":top1"]]
	if !top.Busy || top.Waiting {
		t.Errorf("top1 busy=%v waiting=%v, want busy", top.Busy, top.Waiting)
	}
	if top.Name != "Benchmark summary" || top.Repo != "bench" || top.Client != "cli" || top.Model != "hermes-4-405b" {
		t.Errorf("top1 fields: %+v", top)
	}
	if top.Intent != "Running the compare script" {
		t.Errorf("top1 intent: %q", top.Intent)
	}
	if cont := got[byID["hermes:"+cfg.Host()+":cont1"]]; cont.Name != "Continued chat" || cont.Busy || len(cont.Subagents) != 0 {
		t.Errorf("continued session: %+v", cont)
	}
	if top.LastUser != "Compare the benchmark with the baseline." || top.LastAssistant != "I read the results." {
		t.Errorf("top1 text: %q / %q", top.LastUser, top.LastAssistant)
	}
	if len(top.RunningTools) != 1 || top.RunningTools[0].Name != "terminal" || top.RunningTools[0].Sink != "127.0.0.1: Build / run" {
		t.Errorf("top1 running tools: %+v", top.RunningTools)
	}
	if top.Tokens.In != 1200 || top.Tokens.Out != 300 || top.ModelTokens["hermes-4-405b"].Calls != 4 {
		t.Errorf("top1 tokens: %+v %+v", top.Tokens, top.ModelTokens["hermes-4-405b"])
	}
	var fsFlow, llmFlow, webFlow bool
	for _, f := range top.Flows {
		fsFlow = fsFlow || f.Sink == "127.0.0.1: Filesystem" && f.Tool == "read_file"
		llmFlow = llmFlow || f.Sink == "LLM: hermes-4-405b"
		webFlow = webFlow || f.Sink == "Web" && f.AgentID == "hermes:sub1"
	}
	if !fsFlow || !llmFlow || webFlow {
		t.Errorf("top1 flows fs=%v llm=%v web=%v (the web call is still open): %+v", fsFlow, llmFlow, webFlow, top.Flows)
	}
	if len(top.Subagents) != 2 {
		t.Fatalf("top1 sub-agents: %+v", top.Subagents)
	}
	sub := top.Subagents[1]
	if old := top.Subagents[0]; old.ID != "hermes:sub2" || old.Running {
		t.Errorf("old sub-agent: %+v", old)
	}
	if sub.ID != "hermes:sub1" || !sub.Running || sub.Type != "delegate_task" || !strings.HasPrefix(sub.Doing, "web_extract") {
		t.Errorf("running sub-agent: %+v", sub)
	}

	ask := got[byID["hermes:"+cfg.Host()+":top2"]]
	if !ask.Waiting || ask.Busy || len(ask.RunningTools) != 1 || ask.RunningTools[0].Sink != "You" {
		t.Errorf("top2 must wait for the user: %+v", ask)
	}
	if ask.Name != "Need your input" || ask.Client != "matrix" {
		t.Errorf("top2 fields: %+v", ask)
	}
}

func TestDashboardRelogin(t *testing.T) {
	m := &mock{}
	_, cfg := startMock(t, m)
	src := New(cfg)
	if err := src.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.expire = true
	m.mu.Unlock()
	if err := src.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m.logins != 2 {
		t.Fatalf("logins = %d, want 2 after a 401", m.logins)
	}
}

func TestDashboardWrongPassword(t *testing.T) {
	m := &mock{}
	_, cfg := startMock(t, m)
	cfg.Password = "wrong"
	src := New(cfg)
	err := src.Refresh(context.Background())
	if err != errUnauthorized {
		t.Fatalf("err = %v, want errUnauthorized", err)
	}
	if strings.Contains(err.Error(), "wrong") || strings.Contains(err.Error(), testPass) {
		t.Fatal("the error contains the password")
	}
	if src.Sessions(time.Now().UnixMilli()) != nil {
		t.Fatal("sessions without data")
	}
}

func TestAPIServer(t *testing.T) {
	m := &mock{token: "tok-123", noSort: true}
	_, cfg := startMock(t, m)
	src := New(cfg)
	if err := src.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := src.Sessions(time.Now().UnixMilli())
	if len(got) != 3 {
		t.Fatalf("got %d sessions, want 3", len(got))
	}
	if m.logins != 0 {
		t.Fatal("API server must not log in")
	}
	var retried bool
	for _, p := range m.paths {
		retried = retried || strings.HasPrefix(p, "GET /api/sessions?") && !strings.Contains(p, "order=")
	}
	if !retried {
		t.Errorf("no retry without order: %v", m.paths)
	}

	cfg.Token = "bad"
	bad := New(cfg)
	if err := bad.Refresh(context.Background()); err != errUnauthorized {
		t.Fatalf("bad token: %v", err)
	}
}

func TestUnreachable(t *testing.T) {
	cfg, _ := LoadConfig("", "http://127.0.0.1:1", func(string) string { return "" })
	src := New(cfg)
	start := time.Now()
	if err := src.Refresh(context.Background()); err == nil {
		t.Fatal("no error")
	}
	if src.Sessions(time.Now().UnixMilli()) != nil {
		t.Fatal("sessions from an unreachable host")
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("too slow")
	}
}

func TestNotHermes(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	cfg, _ := LoadConfig("", srv.URL, func(string) string { return "" })
	err := New(cfg).Refresh(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not a Hermes") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hermes.json")
	none := func(string) string { return "" }

	if cfg, err := LoadConfig(path, "", none); cfg != nil || err != nil {
		t.Fatalf("no file and no URL: %v %v", cfg, err)
	}
	if err := os.WriteFile(path, []byte(`{"url":"https://h.example.com/sessions?profile=p1","username":"u","password":"pw"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path, "", none)
	if err != nil || cfg.URL != "https://h.example.com" || cfg.Profile != "p1" || cfg.Username != "u" || cfg.Password != "pw" || cfg.Host() != "h.example.com" {
		t.Fatalf("file: %+v %v", cfg, err)
	}
	env := map[string]string{EnvURL: "h2:9119", EnvToken: " t "}
	cfg, err = LoadConfig(path, "", func(k string) string { return env[k] })
	if err != nil || cfg.URL != "http://h2:9119" || cfg.Token != "t" || cfg.Password != "pw" {
		t.Fatalf("env: %+v %v", cfg, err)
	}
	cfg, err = LoadConfig(path, "https://flag.example.com:8443", func(k string) string { return env[k] })
	if err != nil || cfg.URL != "https://flag.example.com:8443" {
		t.Fatalf("flag: %+v %v", cfg, err)
	}
	if _, err := LoadConfig("", "ftp://x", none); err == nil {
		t.Fatal("ftp URL accepted")
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := LoadConfig(path, "", none)
		if err == nil || !strings.Contains(err.Error(), "chmod 600") || strings.Contains(err.Error(), "pw") {
			t.Fatalf("0644 file: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte(`{"password":"leak"`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(filepath.Join(dir, "bad.json"), "", none); err == nil || strings.Contains(err.Error(), "leak") {
		t.Fatalf("bad JSON: %v", err)
	}
}

func TestInsecure(t *testing.T) {
	for _, c := range []struct {
		url, token string
		want       bool
	}{
		{"http://agent.example.com:9119", "t", true},
		{"https://agent.example.com", "t", false},
		{"http://127.0.0.1:9119", "t", false},
		{"http://localhost:9119", "t", false},
		{"http://agent.example.com:9119", "", false},
	} {
		cfg := &Config{URL: c.url, Token: c.token}
		if err := cfg.normalize(); err != nil {
			t.Fatal(err)
		}
		if cfg.insecure() != c.want {
			t.Errorf("%s token=%q: insecure=%v", c.url, c.token, !c.want)
		}
	}
}

func TestFlexTime(t *testing.T) {
	want := int64(1_700_000_000_500)
	for _, in := range []string{`1700000000.5`, `1700000000500`, `"2023-11-14T22:13:20.5Z"`, `"1700000000.5"`} {
		var ft flexTime
		if err := json.Unmarshal([]byte(in), &ft); err != nil || int64(ft) != want {
			t.Errorf("%s: %d %v", in, ft, err)
		}
	}
	var ft flexTime
	if err := json.Unmarshal([]byte(`null`), &ft); err != nil || ft != 0 {
		t.Errorf("null: %d %v", ft, err)
	}
}

func TestSink(t *testing.T) {
	cfg := &Config{URL: "https://agent.example.com"}
	_ = cfg.normalize()
	s := &Source{cfg: cfg}
	for _, c := range []struct {
		tool string
		args map[string]any
		want string
	}{
		{"terminal", map[string]any{"command": "ls -la"}, "agent: Shell"},
		{"terminal", map[string]any{"command": "git status"}, "agent: Git"},
		{"terminal", map[string]any{"command": "curl https://example.com"}, "Web"},
		{"write_file", nil, "agent: Filesystem"},
		{"browser_navigate", nil, "Web"},
		{"clarify", nil, "You"},
		{"memory", nil, "agent: Memory"},
		{"mcp_github_list_issues", nil, "MCP: unknown server"},
		{"mcp__demo_server__browser_navigate", nil, "MCP: demo_server"},
		{"delegate_task", nil, ""},
		{"something_new", nil, "agent: Tools"},
	} {
		if got := s.sink(c.tool, c.args); got != c.want {
			t.Errorf("sink(%s, %v) = %q, want %q", c.tool, c.args, got, c.want)
		}
	}
	if shortHost("192.0.2.10") != "192.0.2.10" || shortHost("a.b.c") != "a" {
		t.Error("shortHost")
	}
}

func TestCronJobs(t *testing.T) {
	m := &mock{}
	_, cfg := startMock(t, m)
	now := m.now
	iso := func(sec float64) string { return time.UnixMilli(int64(sec * 1000)).Format(time.RFC3339Nano) }
	m.jobs = []map[string]any{
		// Running, no session row yet: the radar makes a session from the job.
		{"id": "j1", "name": "Nightly build", "model": "hermes-4-70b", "workdir": "/srv/build",
			"latest_execution": map[string]any{"status": "running", "started_at": iso(now - 20), "finished_at": nil}},
		// Running, with a session row: the row is busy, also if the last message is from the assistant.
		{"id": "j2", "name": "Report",
			"latest_execution": map[string]any{"status": "running", "started_at": iso(now - 40), "finished_at": nil}},
		// Done.
		{"id": "j3", "name": "Backup", "latest_execution": map[string]any{"status": "completed", "started_at": iso(now - 100), "finished_at": iso(now - 90)}},
		// A claim without an end for hours: abandoned.
		{"id": "j4", "name": "Stuck", "latest_execution": map[string]any{"status": "claimed", "claimed_at": iso(now - 3*3600), "finished_at": nil}},
		// No run yet.
		{"id": "j5", "name": "New", "model": map[string]any{"name": "x"}},
	}
	m.extra = []map[string]any{
		{"id": "rep", "source": "cron", "title": "Report · Nov 07 16:00", "started_at": now - 40, "last_active": now - 30, "message_count": 2, "is_active": true},
		{"id": "bak", "source": "cron", "title": "Backup · Nov 07 15:58", "started_at": now - 100, "last_active": now - 90, "ended_at": now - 90, "message_count": 2},
		// An empty cron row that Hermes did not close. Hermes calls it active for 5 minutes.
		{"id": "empty1", "source": "cron", "title": nil, "preview": "", "started_at": now - 30, "last_active": now - 30, "message_count": 0, "is_active": true},
		// An untitled cron row with messages.
		{"id": "untitled", "source": "cron", "preview": "SYSTEM: scheduled job instructions.", "started_at": now - 500, "last_active": now - 400, "ended_at": now - 400, "message_count": 2},
		// A chat that answered one minute ago. Hermes calls it active, but it waits for the user.
		{"id": "chat", "source": "matrix", "title": "Chat", "started_at": now - 300, "last_active": now - 60, "message_count": 2, "is_active": true},
	}
	m.msgs = map[string][]map[string]any{
		"rep":  {{"role": "user", "content": "Write the report.", "timestamp": now - 40}, {"role": "assistant", "content": "Step one is done.", "timestamp": now - 30}},
		"chat": {{"role": "user", "content": "Hi", "timestamp": now - 70}, {"role": "assistant", "content": "Hello.", "timestamp": now - 60}},
	}
	src := New(cfg)
	if err := src.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := src.Sessions(time.Now().UnixMilli())
	by := map[string]*collector.Session{}
	for _, s := range got {
		by[strings.TrimPrefix(s.ID, "hermes:"+cfg.Host()+":")] = s
	}
	if s := by["cron:j1"]; s == nil || !s.Busy || !s.Live || s.Name != "cron · Nightly build" || s.Client != "cron" || s.Model != "hermes-4-70b" || s.Repo != "build" {
		t.Errorf("running job without a row: %+v", s)
	}
	if s := by["rep"]; s == nil || !s.Busy {
		t.Errorf("running job with a row must be busy: %+v", s)
	}
	if by["cron:j2"] != nil {
		t.Error("a job with a session row must not show two times")
	}
	if s := by["bak"]; s == nil || s.Busy {
		t.Errorf("finished job: %+v", s)
	}
	for _, id := range []string{"cron:j3", "cron:j4", "cron:j5", "empty1"} {
		if by[id] != nil {
			t.Errorf("%s must not show: %+v", id, by[id])
		}
	}
	if s := by["untitled"]; s == nil || !strings.HasPrefix(s.Name, "cron · ") || strings.Contains(s.Name, "SYSTEM") {
		t.Errorf("untitled cron name: %+v", s)
	}
	if s := by["chat"]; s == nil || s.Busy {
		t.Errorf("is_active alone must not make a chat busy: %+v", s)
	}
	if !got[0].Busy {
		t.Errorf("busy sessions must come first: %s", got[0].ID)
	}
}

func TestBusyFirstUnderLimit(t *testing.T) {
	m := &mock{}
	_, cfg := startMock(t, m)
	now := m.now
	for i := range maxShown + 5 {
		m.extra = append(m.extra, map[string]any{"id": "idle" + strconv.Itoa(i), "source": "cli", "title": "Idle", "started_at": now - 50, "last_active": now - 1, "ended_at": now - 1})
	}
	m.jobs = []map[string]any{{"id": "j1", "name": "Long job",
		"latest_execution": map[string]any{"status": "running", "started_at": time.UnixMilli(int64((now - 3000) * 1000)).Format(time.RFC3339), "finished_at": nil}}}
	src := New(cfg)
	if err := src.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := src.Sessions(time.Now().UnixMilli())
	if len(got) != maxShown {
		t.Fatalf("got %d sessions, want %d", len(got), maxShown)
	}
	found := false
	for _, s := range got {
		found = found || s.Name == "cron · Long job"
	}
	if !found {
		t.Error("the running job must not be cut by the limit")
	}
}

func TestNoCronAPI(t *testing.T) {
	m := &mock{} // jobs nil: /api/cron/jobs gives 404
	_, cfg := startMock(t, m)
	src := New(cfg)
	if err := src.Refresh(context.Background()); err != nil {
		t.Fatalf("a missing cron API must not fail the refresh: %v", err)
	}
	if n := len(src.Sessions(time.Now().UnixMilli())); n != 3 {
		t.Errorf("got %d sessions, want 3", n)
	}
}

func TestDelegates(t *testing.T) {
	m := &mock{config: map[string]any{"delegation": map[string]any{
		"model": "model-b", "provider": "p1", "api_key": "not-read", "max_concurrent_children": 2, "backend": "map",
		"backends": map[string]any{"map": map[string]any{"default": "main", "options": []any{
			map[string]any{"name": "main", "model": "model-main", "provider": "p1"},
			map[string]any{"name": "deep", "model": "model-deep", "provider": "p2"},
		}}}}}}
	_, cfg := startMock(t, m)
	now := m.now
	call := func(id string, args string, at float64) map[string]any {
		return map[string]any{"role": "assistant", "content": "", "timestamp": at,
			"tool_calls": []map[string]any{{"id": id, "function": map[string]any{"name": "delegate_task", "arguments": args}}}}
	}
	result := func(id, content string, at float64) map[string]any {
		return map[string]any{"role": "tool", "tool_call_id": id, "tool_name": "delegate_task", "content": content, "timestamp": at}
	}
	m.extra = []map[string]any{
		// The last activity is 30 minutes old: the first read must still take its messages.
		{"id": "par", "source": "matrix", "title": "Review", "model": "model-p", "started_at": now - 4000, "last_active": now - 1780,
			"message_count": 9, "input_tokens": 100, "output_tokens": 10, "api_call_count": 2},
		{"id": "done", "source": "matrix", "title": "Ended", "model": "model-p", "started_at": now - 600, "last_active": now - 100, "ended_at": now - 100, "message_count": 3},
	}
	m.msgs = map[string][]map[string]any{
		"par": {
			call("old", `{"goal":"Old task","option":"deep"}`, now-3501),
			result("old", `{"status": "dispatched", "mode": "background", "count": 1, "delegation_id": "deleg_old1", "goals": ["Old task"]}`, now-3500),
			{"role": "user", "content": "[ASYNC DELEGATION COMPLETE — deleg_old1]\nStatus: error   API calls: 7   Duration: 9s\n--- RESULT ---\nThe subagent did not complete successfully (status=error).", "timestamp": now - 3000},
			call("sync", `{"goal":"Quick look"}`, now-2501),
			result("sync", `{"results": [{"status": "completed", "summary": "ok"}]}`, now-2500),
			call("d1", `{"tasks":[{"goal":"Check the docs","option":"deep"},{"goal":"Run the tests"},{"goal":"Pinned","model":"model-x","option":"deep"}]}`, now-1790),
			result("d1", `{"status": "dispatched", "mode": "background", "count": 3, "delegation_id": "deleg_new1", "goals": ["Check the docs", "Run the tests", "Pinned"]}`, now-1785),
			{"role": "assistant", "content": "Three helpers work on it.", "timestamp": now - 1780},
			// One part of the batch ends: task 3 only.
			{"role": "user", "content": "[ASYNC DELEGATION BATCH COMPLETE — deleg_new1]\nA background fan-out unit\n\n--- ✓ TASK 3/3: Pinned  (status=completed, api_calls=4, 12.5s) ---\nok", "timestamp": now - 1779},
			{"role": "user", "content": "Is deleg_new1 done?", "timestamp": now - 1778}, // no completion marker
		},
		"done": {
			call("d2", `{"goal":"Lost task"}`, now-201),
			result("d2", `{"status": "dispatched", "delegation_id": "deleg_lost", "goal": "Lost task"}`, now-200),
		},
	}
	src := New(cfg)
	for range 2 { // the second read keeps the quiet parent
		if err := src.Refresh(context.Background()); err != nil {
			t.Fatal(err)
		}
		by := map[string]*collector.Session{}
		for _, s := range src.Sessions(time.Now().UnixMilli()) {
			by[strings.TrimPrefix(s.ID, "hermes:"+cfg.Host()+":")] = s
		}
		p := by["par"]
		if p == nil || !p.Busy {
			t.Fatalf("a parent with running delegates must be busy: %+v", p)
		}
		subs := map[string]collector.Subagent{}
		for _, sa := range p.Subagents {
			subs[strings.TrimPrefix(sa.ID, "hermes:par:")] = sa
		}
		if sa := subs["deleg_new1:0"]; !sa.Running || sa.Model != "model-deep" || sa.Name != "Check the docs" || sa.Type != "delegate_task" || sa.StartedAt != int64((now-1785)*1000) {
			t.Errorf("a task with an option must use the model of the option: %+v", sa)
		}
		if sa := subs["deleg_new1:1"]; !sa.Running || sa.Model != "model-main" || sa.Name != "Run the tests" {
			t.Errorf("a task without an option must use the default option: %+v", sa)
		}
		if sa := subs["deleg_new1:2"]; sa.Running || sa.Failed || sa.Model != "model-x" || sa.EndedAt != int64((now-1779)*1000) {
			t.Errorf("a pinned task that ended in a batch part: %+v", sa)
		}
		if sa := subs["deleg_old1:0"]; sa.Running || !sa.Failed || sa.Model != "model-deep" || sa.EndedAt != int64((now-3000)*1000) {
			t.Errorf("finished delegate: %+v", sa)
		}
		if len(subs) != 4 {
			t.Errorf("a synchronous delegate_task result is not a background delegate: %v", subs)
		}
		if u := p.ModelTokens["model-deep"]; u == nil || u.Calls != 7 {
			t.Errorf("delegate calls must count for the delegate model: %+v", p.ModelTokens)
		}
		if u := p.ModelTokens["model-x"]; u == nil || u.Calls != 4 {
			t.Errorf("batch task calls: %+v", p.ModelTokens)
		}
		if u := p.ModelTokens["model-p"]; u == nil || u.Calls != 2 || u.In != 100 {
			t.Errorf("parent tokens must stay with the parent model: %+v", p.ModelTokens)
		}
		if d := by["done"]; d == nil || d.Busy || len(d.Subagents) != 0 {
			t.Errorf("an ended parent has no running delegates: %+v", d)
		}
	}
}

func TestDelegateModel(t *testing.T) {
	msgs := []message{
		{Role: "tool", ToolName: "delegate_task", Content: json.RawMessage(`"<untrusted_tool_result>{\"status\": \"dispatched\", \"delegation_id\": \"deleg_x\", \"count\": 2}"`), Timestamp: 1000},
	}
	ds := delegations(msgs)
	if len(ds) != 1 || len(ds[0].tasks) != 2 {
		t.Fatalf("delegations: %+v", ds)
	}
	dr := delegates("s", ds, nil, "model-p", false, 2000, nil, nil)
	subs := dr.subs
	if !dr.running || len(subs) != 2 || subs[0].Model != unknownModel || subs[0].Name != "delegate" {
		t.Errorf("without the config the model is unknown, not the parent model: %+v", subs)
	}
	subs = delegates("s", ds, &delegConfig{}, "model-p", false, 2000, nil, nil).subs
	if subs[0].Model != "model-p" {
		t.Errorf("an empty delegation.model means: the parent model: %+v", subs)
	}
	subs = delegates("s", ds, &delegConfig{Model: "model-b", Backend: "none"}, "model-p", false, 2000, nil, nil).subs
	if subs[0].Model != "model-b" {
		t.Errorf("delegation.model: %+v", subs)
	}
	if delegates("s", ds, nil, "model-p", false, 1000+delegWindow+1, nil, nil).running {
		t.Error("a delegation without an end must stop after delegWindow")
	}
}

func TestDelegateTools(t *testing.T) {
	m := &mock{config: map[string]any{"delegation": map[string]any{"backend": "map",
		"backends": map[string]any{"map": map[string]any{"default": "main", "options": []any{
			map[string]any{"name": "main", "model": "model-main"},
			map[string]any{"name": "deep", "model": "model-deep"},
		}}}}}}
	_, cfg := startMock(t, m)
	now := m.now
	m.extra = []map[string]any{{"id": "par", "source": "matrix", "title": "Review", "model": "model-p", "started_at": now - 400, "last_active": now - 290}}
	m.kids = []map[string]any{
		{"id": "other", "source": "subagent", "parent_session_id": "elsewhere", "model": "model-z", "preview": "Check the docs now", "started_at": now - 280},
		{"id": "early", "source": "subagent", "parent_session_id": "par", "model": "model-z", "preview": "Check the docs again", "started_at": now - 900},
		{"id": "k1", "source": "subagent", "parent_session_id": "par", "model": "model-real", "preview": "Check the docs now",
			"started_at": now - 290, "last_active": now - 3, "input_tokens": 50, "output_tokens": 5, "api_call_count": 3},
	}
	m.msgs = map[string][]map[string]any{
		"par": {
			{"role": "assistant", "content": "", "timestamp": now - 300, "tool_calls": []map[string]any{{"id": "d", "function": map[string]any{"name": "delegate_task",
				"arguments": `{"tasks":[{"goal":"Check the docs now","option":"deep"},{"goal":"Run the tests"}]}`}}}},
			{"role": "tool", "tool_call_id": "d", "tool_name": "delegate_task", "timestamp": now - 299,
				"content": `{"status": "dispatched", "mode": "background", "count": 2, "delegation_id": "deleg_t", "goals": ["Check the docs now", "Run the tests"]}`},
		},
		"k1": {
			{"role": "user", "content": "Check the docs now", "timestamp": now - 290},
			{"role": "assistant", "timestamp": now - 20, "tool_calls": []map[string]any{{"id": "w", "function": map[string]any{"name": "web_extract", "arguments": map[string]any{"urls": []string{"https://example.com/docs"}}}}}},
			{"role": "tool", "tool_call_id": "w", "tool_name": "web_extract", "content": "text", "timestamp": now - 15},
			{"role": "assistant", "timestamp": now - 3, "tool_calls": []map[string]any{{"id": "g", "function": map[string]any{"name": "mcp__srv__get_issue", "arguments": map[string]any{"number": 1}}}}},
		},
	}
	src := New(cfg)
	for round := range 2 {
		if err := src.Refresh(context.Background()); err != nil {
			t.Fatal(err)
		}
		var p *collector.Session
		for _, s := range src.Sessions(time.Now().UnixMilli()) {
			if strings.HasSuffix(s.ID, ":par") {
				p = s
			}
		}
		if p == nil || !p.Busy {
			t.Fatalf("round %d: parent: %+v", round, p)
		}
		subs := map[string]collector.Subagent{}
		for _, sa := range p.Subagents {
			subs[strings.TrimPrefix(sa.ID, "hermes:par:")] = sa
		}
		a := subs["deleg_t:0"]
		if !a.Running || a.Model != "model-real" || !strings.HasPrefix(a.Doing, "mcp__srv__get_issue") {
			t.Errorf("round %d: the child session gives the model and the current tool: %+v", round, a)
		}
		if b := subs["deleg_t:1"]; !b.Running || b.Model != "model-main" || b.Doing != "background task" {
			t.Errorf("round %d: a task without a found child: %+v", round, b)
		}
		tool := false
		for _, x := range p.RunningTools {
			tool = tool || (x.Name == "mcp__srv__get_issue" && x.AgentID == "hermes:par:deleg_t:0" && x.Sink != "")
		}
		if !tool {
			t.Errorf("round %d: the parent must show the open delegate tool: %+v", round, p.RunningTools)
		}
		flow := false
		for _, f := range p.Flows {
			flow = flow || (f.Tool == "web_extract" && f.AgentID == "hermes:par:deleg_t:0")
		}
		if !flow {
			t.Errorf("round %d: the parent must show the delegate flows: %+v", round, p.Flows)
		}
		if u := p.ModelTokens["model-real"]; u == nil || u.In != 50 || u.Out != 5 || u.Calls != 3 {
			t.Errorf("round %d: child tokens must count for the child model: %+v", round, p.ModelTokens)
		}
	}
	detail := false
	for _, x := range m.paths {
		detail = detail || strings.HasPrefix(x, "GET /api/sessions/k1?")
		if strings.HasPrefix(x, "POST") && !strings.Contains(x, "password-login") {
			t.Errorf("only GET requests: %s", x)
		}
	}
	if !detail {
		t.Error("the second read must refresh the known child with the detail route")
	}
}

func TestFindChild(t *testing.T) {
	m := &mock{}
	_, cfg := startMock(t, m)
	now := m.now
	goal := "Check the open issues of the first project and list them by age"
	m.kids = []map[string]any{
		{"id": "a", "source": "subagent", "parent_session_id": "p", "preview": "Check the open issues of the first project and list them by label", "started_at": now - 50},
		{"id": "b", "source": "subagent", "parent_session_id": "p", "preview": goal, "started_at": now - 40},
	}
	c := newClient(cfg)
	at := int64(now*1000) - 60_000
	if r, ok := c.findChild(context.Background(), "p", goal, at, nil); !ok || r.ID != "b" {
		t.Errorf("the child with the same goal must win: %+v", r)
	}
	if r, ok := c.findChild(context.Background(), "p", goal, at, map[string]bool{"b": true}); !ok || r.ID != "a" {
		t.Errorf("a taken child must not match: %+v", r)
	}
	if _, ok := c.findChild(context.Background(), "q", goal, at, nil); ok {
		t.Error("a child of a different parent must not match")
	}
}

func TestDelegateDoing(t *testing.T) {
	ds := []*delegation{{id: "deleg_a", at: 1000, tasks: []*dtask{{goal: "g"}, {goal: "h"}}}}
	done := collector.Tool{Name: "web_search", Desc: "query"}
	kids := map[string]*kid{
		kidKey("s", "deleg_a", 0): {row: sessionRow{ID: "c0", Model: "model-c"}, act: activity{done: &done}},
		kidKey("s", "deleg_a", 1): {row: sessionRow{ID: "c1", Model: "model-c", EndedAt: 1500}},
	}
	dr := delegates("s", ds, nil, "model-p", false, 2000, nil, kids)
	if len(dr.subs) != 2 || dr.subs[0].Doing != "thinking · last: web_search: query" || dr.subs[0].Model != "model-c" {
		t.Errorf("a delegate between tool calls: %+v", dr.subs)
	}
	if dr.subs[1].Running || dr.subs[1].EndedAt != 1500 {
		t.Errorf("an ended child ends the task: %+v", dr.subs[1])
	}
}
