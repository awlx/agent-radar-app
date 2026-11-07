package hermes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
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
	noSort bool // the server does not know order=recent
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
		envelope("sessions", []map[string]any{
			{"id": "top1", "source": "cli", "model": "hermes-4-405b", "title": "Benchmark summary", "cwd": "/home/me/src/bench",
				"started_at": now - 600, "last_active": now - 2, "input_tokens": 1200.0, "output_tokens": 300, "api_call_count": 4,
				"last_activity_at": now - 1, "last_activity_description": "Running the compare script"},
			{"id": "top2", "source": "matrix", "model": "hermes-4-405b", "preview": "Need your input",
				"started_at": int64(now*1000) - 300_000, "last_active": time.UnixMilli(int64(now*1000) - 10_000).UTC().Format(time.RFC3339)},
			{"id": "old", "source": "cli", "title": "Last week", "started_at": now - 8*86400, "last_active": now - 8*86400, "ended_at": now - 8*86400},
			{"id": "sub1", "source": "subagent", "parent_session_id": "top1"},
			{"id": "cont1", "source": "matrix", "parent_session_id": "top2", "title": "Continued chat", "started_at": now - 7200, "last_active": now - 7000, "ended_at": nil},
		})
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
