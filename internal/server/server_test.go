package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	handler := NewHandler(hub, "", true)
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
