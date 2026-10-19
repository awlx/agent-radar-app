package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

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
