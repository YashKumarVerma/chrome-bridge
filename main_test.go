package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBridge(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "c.yaml")
	os.WriteFile(cfg, []byte(`
out_dir: `+dir+`/data
secret: test-secret
sources:
  - name: mb
    url: https://metabase.famapp.in
    every: 5m
    cookies: [metabase.SESSION]
    write:
      - from: cookie:metabase.SESSION
        to: `+dir+`/session
`), 0o600)
	h := handler(cfg)
	type req struct {
		method, path, origin, body, secret string
		age                                time.Duration
	}
	do := func(q req) *httptest.ResponseRecorder {
		r := httptest.NewRequest(q.method, q.path, strings.NewReader(q.body))
		if q.origin != "" {
			r.Header.Set("Origin", q.origin)
		}
		if q.secret == "" {
			q.secret = "test-secret"
		}
		ts := strconv.FormatInt(time.Now().Add(-q.age).Unix(), 10)
		r.Header.Set("X-Chrome-Bridge-Time", ts)
		r.Header.Set("X-Chrome-Bridge-Sig", sign(q.secret, ts+"\n"+q.method+"\n"+q.path+"\n"+q.body))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code < 300 && w.Header().Get("X-Chrome-Bridge-Sig") != sign("test-secret", "resp\n"+ts+"\n"+w.Body.String()) {
			t.Fatalf("%s %s: response not signed", q.method, q.path)
		}
		return w
	}
	ext := "chrome-extension://abc"

	for name, c := range map[string]struct {
		q    req
		want int
	}{
		"web origin":      {req{method: "GET", path: "/config", origin: "https://evil.example"}, 403},
		"wrong secret":    {req{method: "GET", path: "/config", secret: "nope"}, 401},
		"stale timestamp": {req{method: "GET", path: "/config", age: 2 * time.Minute}, 401},
		"unknown source":  {req{method: "POST", path: "/push", origin: ext, body: `{"name":"../x"}`}, 404},
	} {
		if got := do(c.q).Code; got != c.want {
			t.Fatalf("%s: got %d want %d", name, got, c.want)
		}
	}
	if w := do(req{method: "GET", path: "/config"}); w.Code != 200 || !strings.Contains(w.Body.String(), `"every_seconds":300`) {
		t.Fatalf("config: %d %s", w.Code, w.Body)
	}
	if c := do(req{method: "POST", path: "/push", origin: ext, body: `{"name":"mb","cookies":{"metabase.SESSION":"s3cret"}}`}).Code; c != 204 {
		t.Fatalf("push got %d", c)
	}
	st := do(req{method: "GET", path: "/status"})
	if st.Code != 200 || !strings.Contains(st.Body.String(), `"mb":{"at"`) {
		t.Fatalf("status: %d %s", st.Code, st.Body)
	}
	if strings.Contains(st.Body.String(), "s3cret") || strings.Contains(st.Body.String(), "test-secret") {
		t.Fatal("status leaks values or the shared secret")
	}
	if w := do(req{method: "GET", path: "/data/mb"}); w.Code != 200 || !strings.Contains(w.Body.String(), `"metabase.SESSION": "s3cret"`) {
		t.Fatalf("data/mb: %d %s", w.Code, w.Body)
	}
	if w := do(req{method: "GET", path: "/data"}); w.Code != 200 || !strings.Contains(w.Body.String(), `"mb":{`) {
		t.Fatalf("data: %d %s", w.Code, w.Body)
	}
	if c := do(req{method: "GET", path: "/data/nope"}).Code; c != 404 {
		t.Fatalf("data/nope got %d", c)
	}
	if c := do(req{method: "GET", path: "/data/mb", secret: "nope"}).Code; c != 401 {
		t.Fatalf("unsigned data got %d", c)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "session")); string(b) != "s3cret" {
		t.Fatalf("write target = %q", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "data", "mb.json")); err != nil {
		t.Fatal(err)
	}
}
