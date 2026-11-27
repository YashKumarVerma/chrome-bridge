package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBridge(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "c.yaml")
	os.WriteFile(cfg, []byte(`
out_dir: `+dir+`/data
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
	do := func(method, path, origin, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if origin != "nohdr" {
			r.Header.Set("X-Chrome-Bridge", "1")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	ext := "chrome-extension://abc"

	if c := do("GET", "/config", "https://evil.example", "").Code; c != 403 {
		t.Fatalf("web origin got %d", c)
	}
	if c := do("POST", "/push", "nohdr", `{"name":"mb"}`).Code; c != 403 {
		t.Fatalf("missing header got %d", c)
	}
	if c := do("GET", "/config", "", "").Code; c != 200 {
		t.Fatalf("extension GET without Origin got %d", c)
	}
	if w := do("GET", "/config", ext, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"every_seconds":300`) {
		t.Fatalf("config: %d %s", w.Code, w.Body)
	}
	if c := do("POST", "/push", ext, `{"name":"../x"}`).Code; c != 404 {
		t.Fatalf("unknown source got %d", c)
	}
	if c := do("POST", "/push", ext, `{"name":"mb","cookies":{"metabase.SESSION":"s3cret"}}`).Code; c != 204 {
		t.Fatalf("push got %d", c)
	}
	if w := do("GET", "/status", ext, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"mb":{"at"`) {
		t.Fatalf("status: %d %s", w.Code, w.Body)
	}
	if strings.Contains(do("GET", "/status", ext, "").Body.String(), "s3cret") {
		t.Fatal("status leaks values")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "session")); string(b) != "s3cret" {
		t.Fatalf("write target = %q", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "data", "mb.json")); err != nil {
		t.Fatal(err)
	}
}
