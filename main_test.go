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
    url: https://metabase.example.com
    every: 5m
    cookies: [metabase.SESSION]
    write:
      - from: cookie:metabase.SESSION
        to: `+dir+`/session
logins:
  - name: jc
    match: https://console.jumpcloud.com/login
    keychain: jc-test
    totp_seed: GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ
vpn:
  probe: http://127.0.0.1:1
  every: 10ms
  cmd: [touch, `+dir+`/reconnected]
`), 0o600)
	keychain = func(s, a string) (string, error) {
		if a == "totp_seed" {
			t.Fatal("totp_seed in YAML must win over Keychain")
		}
		return s + "/" + a, nil
	}
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
	if w := do(req{method: "GET", path: "/logins"}); w.Code != 200 || w.Body.String() != `[{"name":"jc","match":"https://console.jumpcloud.com/login"}]` {
		t.Fatalf("logins: %d %s", w.Code, w.Body)
	}
	if w := do(req{method: "POST", path: "/login/jc"}); w.Code != 200 || w.Body.String() != `{"email":"jc-test/email","password":"jc-test/password"}` {
		t.Fatalf("login: %d %s", w.Code, w.Body)
	}
	if c := do(req{method: "POST", path: "/login/nope"}).Code; c != 404 {
		t.Fatalf("unknown login got %d", c)
	}
	if w := do(req{method: "POST", path: "/login/jc/otp"}); w.Code != 200 || !strings.HasPrefix(w.Body.String(), `{"otp":"`) {
		t.Fatalf("otp: %d %s", w.Code, w.Body)
	}
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(filepath.Join(dir, "reconnected")); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(dir, "reconnected")); err != nil {
		t.Fatal("vpn cmd not run after probe failures")
	}
	if st := do(req{method: "GET", path: "/status"}); !strings.Contains(st.Body.String(), `"vpn":{"up":false`) {
		t.Fatalf("status vpn: %s", st.Body)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "session")); string(b) != "s3cret" {
		t.Fatalf("write target = %q", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "data", "mb.json")); err != nil {
		t.Fatal(err)
	}
}

func TestTOTP(t *testing.T) {
	for ts, want := range map[int64]string{59: "287082", 1111111109: "081804", 1234567890: "005924"} {
		if got, err := totp("gezdgnbvgy3tqojqgezdgnbvgy3tqojq", time.Unix(ts, 0)); err != nil || got != want {
			t.Fatalf("t=%d: got %q %v want %s", ts, got, err, want)
		}
	}
}
