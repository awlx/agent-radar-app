package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/awlx/agent-radar-app/internal/collector"
)

func TestBackendIdentityDoesNotCollect(t *testing.T) {
	h := NewHandler(NewHub(snapshotSource{nil}, time.Second), "", true, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "http://127.0.0.1:47900/api/info", nil))
	if w.Code != http.StatusOK || w.Body.String() != `{"app":"agent-radar","schema":1}` {
		t.Fatalf("backend identity: %d %s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "http://evil.example/api/info", nil))
	if w.Code != http.StatusForbidden {
		t.Fatal("backend identity must retain loopback host checks")
	}
}

func TestCachedMenuStatus(t *testing.T) {
	snap := &collector.Snapshot{Now: time.Now().UnixMilli(), Sessions: []*collector.Session{
		{ID: "busy", Busy: true},
		{ID: "demo-wait", Name: "Demo", Surface: "Copilot app", Busy: true, Waiting: true},
		{ID: "idle"},
	}}
	hub := NewHub(snapshotSource{snap}, 2*time.Second)
	h := NewHandler(hub, "", true, nil)
	get := func(host string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "http://"+host+"/api/status", nil))
		return w
	}
	if w := get("127.0.0.1:47900"); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("uninitialized cache must not report idle: %d", w.Code)
	}
	hub.poll()
	w := get("127.0.0.1:47900")
	var got struct {
		Schema, Working, Waiting int
		Waits                    []struct {
			ID       string
			Jumpable bool
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || got.Schema != 1 || got.Working != 1 || got.Waiting != 1 ||
		len(got.Waits) != 1 || got.Waits[0].ID != "demo-wait" || got.Waits[0].Jumpable {
		t.Fatalf("unexpected menu status: %s", w.Body)
	}
	if w := get("evil.example:47900"); w.Code != http.StatusForbidden {
		t.Fatal("status must enforce loopback host checks")
	}
	snap.Now = time.Now().Add(-time.Minute).UnixMilli()
	if w := get("127.0.0.1:47900"); w.Code != http.StatusOK {
		t.Fatal("status unexpectedly recollected")
	}
	hub.poll()
	if w := get("127.0.0.1:47900"); w.Code != http.StatusServiceUnavailable {
		t.Fatal("stale cache must not report live counts")
	}
}
