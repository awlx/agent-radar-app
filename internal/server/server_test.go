package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/awlx/agent-radar-app/internal/collector"
)

type snapshotSource struct{ snapshot *collector.Snapshot }

func (s snapshotSource) Snapshot() *collector.Snapshot { return s.snapshot }

func TestBackgroundShellStateAndSummary(t *testing.T) {
	snap := &collector.Snapshot{Sessions: []*collector.Session{{
		ID: "owner", Busy: false, Live: false,
		BackgroundShells: []collector.BackgroundShell{{ID: "preview", PID: 123, StartedAt: 1000}},
	}}}
	hub := NewHub(snapshotSource{snap}, 0)
	updates := hub.Subscribe()
	defer hub.Unsubscribe(updates)
	hub.poll()
	update := <-updates
	var streamed collector.Snapshot
	if err := json.Unmarshal(update, &streamed); err != nil || len(streamed.Sessions) != 1 || len(streamed.Sessions[0].BackgroundShells) != 1 {
		t.Fatalf("streamed background shells missing: %s, %v", update, err)
	}
	handler := NewHandler(hub, "", true, nil)
	for _, path := range []string{"/state", "/api/snapshot"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", "http://127.0.0.1:47900"+path, nil))
		var got struct {
			Sessions []struct {
				BackgroundShells []collector.BackgroundShell `json:"backgroundShells"`
				Status           string                      `json:"status"`
			} `json:"sessions"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if w.Code != http.StatusOK || len(got.Sessions) != 1 || len(got.Sessions[0].BackgroundShells) != 1 ||
			got.Sessions[0].BackgroundShells[0].ID != "preview" {
			t.Fatalf("%s: background shells lost: %s", path, w.Body)
		}
		if path == "/api/snapshot" && got.Sessions[0].Status != "idle" {
			t.Fatalf("shell-only session incorrectly marked busy: %s", w.Body)
		}
	}
}

func TestJump(t *testing.T) {
	hub := NewHub(snapshotSource{&collector.Snapshot{Sessions: []*collector.Session{{ID: "known"}}}}, 0)
	hub.poll()
	page := func(h http.Handler) string {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "http://127.0.0.1:47900/", nil))
		return w.Body.String()
	}
	post := func(h http.Handler, origin string) int {
		r := httptest.NewRequest("POST", "http://127.0.0.1:47900/api/jump?session=known", nil)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}

	off := NewHandler(hub, "", true, nil)
	if strings.Contains(page(off), "data-jump") || post(off, "http://127.0.0.1:47900") == http.StatusNoContent {
		t.Fatal("jump must be off without a jump function")
	}

	var got []string
	on := NewHandler(hub, "", true, func(state []byte, id string) bool {
		got = append(got, id)
		return id == "known" && strings.Contains(string(state), `"known"`)
	})
	if !strings.Contains(page(on), "<body data-jump>") {
		t.Fatal("the page must tell the UI that jump is on")
	}
	if c := post(on, ""); c != http.StatusForbidden {
		t.Fatalf("no origin: %d", c)
	}
	if c := post(on, "http://evil.example"); c != http.StatusForbidden {
		t.Fatalf("cross origin: %d", c)
	}
	if len(got) != 0 {
		t.Fatalf("rejected requests called jump: %q", got)
	}
	if c := post(on, "http://127.0.0.1:47900"); c != http.StatusNoContent || len(got) != 1 {
		t.Fatalf("same origin: %d, %q", c, got)
	}
	r := httptest.NewRequest("POST", "http://127.0.0.1:47900/api/jump?session=other", nil)
	r.Header.Set("Origin", "http://127.0.0.1:47900")
	w := httptest.NewRecorder()
	on.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown session: %d", w.Code)
	}
}

func TestAuth(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	lan := secure(ok, "s3cret", false)

	do := func(h http.Handler, target string, mod func(*http.Request)) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", target, nil)
		if mod != nil {
			mod(r)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := do(lan, "http://10.0.0.5:47900/state", nil); w.Code != 401 {
		t.Fatalf("no token: %d", w.Code)
	}
	if w := do(lan, "http://10.0.0.5:47900/?token=wrong", nil); w.Code != 401 {
		t.Fatalf("wrong token: %d", w.Code)
	}
	w := do(lan, "http://10.0.0.5:47900/?token=s3cret", nil)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" {
		t.Fatalf("token redirect: %d %q", w.Code, w.Header().Get("Location"))
	}
	c := w.Result().Cookies()
	if len(c) != 1 || !c[0].HttpOnly {
		t.Fatalf("cookie: %#v", c)
	}
	if w := do(lan, "http://10.0.0.5:47900/state", func(r *http.Request) { r.AddCookie(c[0]) }); w.Code != 200 {
		t.Fatalf("cookie auth: %d", w.Code)
	}
	if w := do(lan, "http://10.0.0.5:47900/state", func(r *http.Request) { r.Header.Set("Authorization", "Bearer s3cret") }); w.Code != 200 {
		t.Fatalf("bearer auth: %d", w.Code)
	}

	local := secure(ok, "", true)
	if w := do(local, "http://127.0.0.1:47900/state", nil); w.Code != 200 {
		t.Fatalf("loopback: %d", w.Code)
	}
	if w := do(local, "http://evil.example:47900/state", nil); w.Code != 403 {
		t.Fatalf("rebinding host: %d", w.Code)
	}
}
