package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/awlx/agent-radar-app/internal/collector"
)

//go:embed ui/ui.html
var uiHTML []byte

const cookieName = "agent_radar_token"

func IsLoopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func RandomToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		log.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// ---- Shared poller and SSE fan-out ----

// Source makes a snapshot of the agents. The collector and the demo data are sources.
type Source interface {
	Snapshot() *collector.Snapshot
}

type Hub struct {
	col      Source
	interval time.Duration

	mu      sync.Mutex
	latest  []byte
	clients map[chan []byte]struct{}
}

func NewHub(c Source, interval time.Duration) *Hub {
	return &Hub{col: c, interval: interval, clients: map[chan []byte]struct{}{}}
}

func (h *Hub) Run(ctx context.Context) {
	t := time.NewTicker(h.interval)
	defer t.Stop()
	for {
		h.poll()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (h *Hub) poll() []byte {
	b, err := json.Marshal(h.col.Snapshot())
	if err != nil {
		log.Printf("snapshot: %v", err)
		return h.State()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if string(b) == string(h.latest) {
		return b
	}
	h.latest = b
	for ch := range h.clients {
		select {
		case ch <- b:
		default: // slow client: drop the oldest update and queue the new one
			select {
			case <-ch:
			default:
			}
			ch <- b
		}
	}
	return b
}

func (h *Hub) State() []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.latest
}

func (h *Hub) Subscribe() chan []byte {
	ch := make(chan []byte, 1)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *Hub) Unsubscribe(ch chan []byte) {
	h.mu.Lock()
	delete(h.clients, ch)
	h.mu.Unlock()
}

// ---- HTTP ----

// NewHandler gives the HTTP handler of the radar. If jump is set, the UI can ask the app to show
// the app of a session: jump gets the JSON state and the ID of the session.
func NewHandler(h *Hub, token string, loopback bool, jump func(state []byte, id string) bool) http.Handler {
	mux := http.NewServeMux()
	page := uiHTML
	if jump != nil {
		page = bytes.Replace(uiHTML, []byte("<body>"), []byte("<body data-jump>"), 1)
		mux.HandleFunc("POST /api/jump", func(w http.ResponseWriter, r *http.Request) {
			// Browsers send Origin with each POST, and secure checks it. A request without Origin is not from the UI.
			if r.Header.Get("Origin") == "" {
				http.Error(w, "forbidden origin", http.StatusForbidden)
				return
			}
			if !jump(h.State(), r.URL.Query().Get("session")) {
				http.Error(w, "no app for this session", http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})
	}
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(page)
	})
	mux.HandleFunc("GET /state", func(w http.ResponseWriter, r *http.Request) {
		b := h.State()
		if b == nil {
			b = h.poll()
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(b)
	})
	mux.HandleFunc("GET /events", func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		ch := h.Subscribe()
		defer h.Unsubscribe(ch)
		fmt.Fprint(w, ": hi\n\n")
		if b := h.State(); b != nil {
			fmt.Fprintf(w, "data: %s\n\n", b)
		}
		fl.Flush()
		ka := time.NewTicker(15 * time.Second)
		defer ka.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case b := <-ch:
				fmt.Fprintf(w, "data: %s\n\n", b)
				fl.Flush()
			case <-ka.C:
				fmt.Fprint(w, ": ka\n\n")
				fl.Flush()
			}
		}
	})
	mux.HandleFunc("GET /api/snapshot", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(collector.Summarize(h.col.Snapshot(), r.URL.Query().Get("all") == ""))
	})
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	return secure(mux, token, loopback)
}

func secure(next http.Handler, token string, loopback bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "no-referrer")
		hd.Set("Cache-Control", "no-store")
		hd.Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'")

		// Loopback mode: stop DNS-rebinding pages from reading the data.
		if loopback {
			host := r.Host
			if h, _, err := net.SplitHostPort(host); err == nil {
				host = h
			}
			if !IsLoopbackHost(strings.Trim(host, "[]")) {
				http.Error(w, "forbidden host", http.StatusForbidden)
				return
			}
		}
		// A cross-site page must not read the data, even with a cookie.
		if o := r.Header.Get("Origin"); o != "" {
			if u, err := url.Parse(o); err != nil || u.Host != r.Host {
				http.Error(w, "forbidden origin", http.StatusForbidden)
				return
			}
		}
		if token == "" || r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		if q := r.URL.Query().Get("token"); q != "" && eq(q, token) {
			http.SetCookie(w, &http.Cookie{Name: cookieName, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 30 * 24 * 3600})
			u := *r.URL
			qs := u.Query()
			qs.Del("token")
			u.RawQuery = qs.Encode()
			http.Redirect(w, r, u.RequestURI(), http.StatusSeeOther)
			return
		}
		if c, err := r.Cookie(cookieName); err == nil && eq(c.Value, token) {
			next.ServeHTTP(w, r)
			return
		}
		if a := r.Header.Get("Authorization"); strings.HasPrefix(a, "Bearer ") && eq(strings.TrimPrefix(a, "Bearer "), token) {
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, "unauthorized: open the URL with ?token=… that agent-radar printed at start", http.StatusUnauthorized)
	})
}

func eq(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }
