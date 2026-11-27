package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

type Write struct {
	From string `yaml:"from" json:"-"` // cookie:<name> or localStorage:<key>
	To   string `yaml:"to" json:"-"`
}

type Source struct {
	Name         string   `yaml:"name" json:"name"`
	URL          string   `yaml:"url" json:"url"`
	Every        string   `yaml:"every" json:"-"`
	EverySeconds int      `yaml:"-" json:"every_seconds"`
	Cookies      []string `yaml:"cookies" json:"cookies"`
	LocalStorage []string `yaml:"local_storage" json:"local_storage"`
	Write        []Write  `yaml:"write" json:"-"`
}

type Login struct {
	Name     string `yaml:"name" json:"name"`
	Match    string    `yaml:"match" json:"match"`
	Done     string    `yaml:"done" json:"done,omitempty"`       // optional: IdP portal a finished login lands on; the tab goes back to where the flow started
	Confirm  []Confirm `yaml:"confirm" json:"confirm,omitempty"` // optional: interstitials to click through after login
	Keychain string    `yaml:"keychain" json:"-"`
	TOTPSeed string    `yaml:"totp_seed" json:"-"` // optional; falls back to Keychain account totp_seed
}

// A redirect-confirm page is clicked only when its `next` query param points at NextHost,
// so the site's open-redirect guard still stops every other target.
type Confirm struct {
	Match    string `yaml:"match" json:"match"`
	Click    string `yaml:"click" json:"click"`
	NextHost string `yaml:"next_host" json:"next_host"`
}

type VPN struct {
	Probe string   `yaml:"probe"`
	Every string   `yaml:"every"`
	Cmd   []string `yaml:"cmd"`
}

type Config struct {
	Listen      string   `yaml:"listen"`
	OutDir      string   `yaml:"out_dir"`
	ExtensionID string   `yaml:"extension_id"`
	Secret      string   `yaml:"secret"`
	Sources     []Source `yaml:"sources"`
	Logins      []Login  `yaml:"logins"`
	VPN         VPN      `yaml:"vpn"`
}

type Push struct {
	Name         string            `json:"name"`
	Cookies      map[string]string `json:"cookies"`
	LocalStorage map[string]string `json:"local_storage"`
	Errors       []string          `json:"errors,omitempty"`
	FetchedAt    time.Time         `json:"fetched_at"`
}

func expand(p string) string {
	if strings.HasPrefix(p, "~/") {
		h, _ := os.UserHomeDir()
		return filepath.Join(h, p[2:])
	}
	return p
}

// Read on every request so YAML edits apply without a restart.
func load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c := &Config{Listen: "127.0.0.1:47123", OutDir: "~/.config/chrome-bridge/data"}
	if err := yaml.Unmarshal(b, c); err != nil {
		return nil, err
	}
	if c.Secret == "" {
		return nil, fmt.Errorf("secret is required in %s", path)
	}
	c.OutDir = expand(c.OutDir)
	for i := range c.Sources {
		s := &c.Sources[i]
		d, err := time.ParseDuration(s.Every)
		if err != nil {
			return nil, fmt.Errorf("source %q: every: %w", s.Name, err)
		}
		s.EverySeconds = int(d.Seconds())
	}
	return c, nil
}

func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func sign(secret, msg string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(msg))
	return hex.EncodeToString(m.Sum(nil))
}

// RFC 6238: SHA1, 6 digits, 30s steps.
func totp(seed string, t time.Time) (string, error) {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(strings.TrimRight(strings.ReplaceAll(seed, " ", ""), "=")))
	if err != nil {
		return "", err
	}
	m := hmac.New(sha1.New, key)
	binary.Write(m, binary.BigEndian, uint64(t.Unix()/30))
	s := m.Sum(nil)
	o := s[len(s)-1] & 0xf
	return fmt.Sprintf("%06d", binary.BigEndian.Uint32(s[o:o+4])&0x7fffffff%1000000), nil
}

// Items made with `security add-generic-password` trust /usr/bin/security, so rebuilds of this binary don't re-prompt.
var keychain = func(service, account string) (string, error) {
	out, err := exec.Command("/usr/bin/security", "find-generic-password", "-s", service, "-a", account, "-w").Output()
	if err != nil {
		return "", fmt.Errorf("keychain %s/%s: %w", service, account, err)
	}
	return strings.TrimSpace(string(out)), nil
}

type vpnState struct {
	Up          bool      `json:"up"`
	Since       time.Time `json:"since"`
	LastAttempt time.Time `json:"last_attempt"`
	LastError   string    `json:"last_error"`
	fails       int
	running     bool
	next        time.Time
	backoff     time.Duration
}

// Probes the VPN and reconnects when it's down. Re-reads the YAML each tick; idles without a vpn block.
func watchVPN(cfgPath string, mu *sync.Mutex, st *vpnState) {
	client := &http.Client{Timeout: 5 * time.Second}
	for {
		cfg, err := load(cfgPath)
		if err != nil || cfg.VPN.Probe == "" || len(cfg.VPN.Cmd) == 0 {
			time.Sleep(30 * time.Second)
			continue
		}
		every, _ := time.ParseDuration(cfg.VPN.Every)
		if every <= 0 {
			every = 30 * time.Second
		}
		resp, perr := client.Get(cfg.VPN.Probe)
		if perr == nil {
			resp.Body.Close()
		}
		mu.Lock()
		switch {
		case perr == nil:
			if !st.Up {
				st.Up, st.Since = true, time.Now()
				log.Printf("vpn: up")
			}
			st.fails, st.backoff, st.next = 0, 0, time.Time{}
		default:
			st.fails++
			if st.fails >= 2 && st.Up {
				st.Up, st.Since = false, time.Now()
				log.Printf("vpn: down (%v)", perr)
			}
			if st.fails >= 2 && !st.running && time.Now().After(st.next) {
				st.running, st.LastAttempt = true, time.Now()
				st.backoff = min(max(st.backoff*2, time.Minute), 15*time.Minute)
				st.next = time.Now().Add(st.backoff)
				go func(cmd []string) {
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
					defer cancel()
					log.Printf("vpn: running %v", cmd)
					out, err := exec.CommandContext(ctx, cmd[0], cmd[1:]...).CombinedOutput()
					mu.Lock()
					defer mu.Unlock()
					st.running, st.LastError = false, ""
					if err != nil {
						st.LastError = fmt.Sprintf("%v: %s", err, strings.TrimSpace(string(out)))
						log.Printf("vpn: reconnect failed: %s", st.LastError)
					}
				}(cfg.VPN.Cmd)
			}
		}
		mu.Unlock()
		time.Sleep(every)
	}
}

// Max clock skew between extension and server for a signed request.
const skew = 60

type lastPush struct {
	At           time.Time `json:"at"`
	Cookies      int       `json:"cookies"`
	LocalStorage int       `json:"local_storage"`
	Errors       []string  `json:"errors"`
}

func handler(cfgPath string) http.Handler {
	mux := http.NewServeMux()
	started := time.Now()
	var mu sync.Mutex
	pushes := map[string]lastPush{}
	vpn := &vpnState{}
	go watchVPN(cfgPath, &mu, vpn)
	lastOTP := map[string]int64{}
	type fn func(r *http.Request, cfg *Config, body []byte) (int, []byte)
	// The secret never crosses the wire: requests carry HMAC(secret, time+method+path+body), and
	// responses HMAC(secret, "resp"+time+body) so the extension can tell this server from a port squatter.
	guard := func(next fn) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			cfg, err := load(cfgPath)
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 5<<20))
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			deny := func(code int, why string) {
				log.Printf("%d %s %s origin=%q: %s", code, r.Method, r.URL.Path, r.Header.Get("Origin"), why)
				http.Error(w, http.StatusText(code)+": "+why, code)
			}
			// Chrome omits Origin on extension GETs, so it's only checked when present.
			if o := r.Header.Get("Origin"); o != "" {
				if !strings.HasPrefix(o, "chrome-extension://") {
					deny(403, "non-extension origin")
					return
				}
				if cfg.ExtensionID != "" && o != "chrome-extension://"+cfg.ExtensionID {
					deny(403, "extension_id mismatch")
					return
				}
			}
			ts := r.Header.Get("X-Chrome-Bridge-Time")
			t, err := strconv.ParseInt(ts, 10, 64)
			if err != nil || time.Since(time.Unix(t, 0)).Abs() > skew*time.Second {
				deny(401, "missing or stale X-Chrome-Bridge-Time")
				return
			}
			want := sign(cfg.Secret, ts+"\n"+r.Method+"\n"+r.URL.Path+"\n"+string(body))
			if !hmac.Equal([]byte(want), []byte(r.Header.Get("X-Chrome-Bridge-Sig"))) {
				deny(401, "bad signature (shared secret mismatch?)")
				return
			}
			code, resp := next(r, cfg, body)
			w.Header().Set("X-Chrome-Bridge-Sig", sign(cfg.Secret, "resp\n"+ts+"\n"+string(resp)))
			w.Header().Set("Access-Control-Expose-Headers", "X-Chrome-Bridge-Sig")
			if code != 204 {
				w.Header().Set("Content-Type", "application/json")
			}
			w.WriteHeader(code)
			w.Write(resp)
		}
	}
	mux.HandleFunc("GET /config", guard(func(r *http.Request, cfg *Config, _ []byte) (int, []byte) {
		b, _ := json.Marshal(cfg.Sources)
		return 200, b
	}))
	mux.HandleFunc("GET /status", guard(func(r *http.Request, cfg *Config, _ []byte) (int, []byte) {
		mu.Lock()
		defer mu.Unlock()
		b, _ := json.Marshal(map[string]any{
			"started_at": started, "config": cfgPath, "out_dir": cfg.OutDir, "listen": cfg.Listen,
			"sources": cfg.Sources, "pushes": pushes, "vpn": vpn,
		})
		return 200, b
	}))
	mux.HandleFunc("GET /logins", guard(func(r *http.Request, cfg *Config, _ []byte) (int, []byte) {
		b, _ := json.Marshal(cfg.Logins)
		return 200, b
	}))
	findLogin := func(cfg *Config, name string) *Login {
		for i := range cfg.Logins {
			if cfg.Logins[i].Name == name {
				return &cfg.Logins[i]
			}
		}
		return nil
	}
	mux.HandleFunc("POST /login/{name}", guard(func(r *http.Request, cfg *Config, _ []byte) (int, []byte) {
		l := findLogin(cfg, r.PathValue("name"))
		if l == nil {
			return 404, []byte("unknown login")
		}
		email, err := keychain(l.Keychain, "email")
		if err != nil {
			return 500, []byte(err.Error())
		}
		pw, err := keychain(l.Keychain, "password")
		if err != nil {
			return 500, []byte(err.Error())
		}
		log.Printf("login %s: credentials served", l.Name)
		b, _ := json.Marshal(map[string]string{"email": email, "password": pw})
		return 200, b
	}))
	mux.HandleFunc("POST /login/{name}/otp", guard(func(r *http.Request, cfg *Config, _ []byte) (int, []byte) {
		l := findLogin(cfg, r.PathValue("name"))
		if l == nil {
			return 404, []byte("unknown login")
		}
		seed, err := l.TOTPSeed, error(nil)
		if seed == "" {
			seed, err = keychain(l.Keychain, "totp_seed")
		}
		if err != nil {
			return 500, []byte(err.Error())
		}
		// A code JumpCloud has already accepted is rejected on reuse, so wait out the window.
		mu.Lock()
		if lastOTP[l.Name] == time.Now().Unix()/30 {
			wait := time.Duration(30-time.Now().Unix()%30) * time.Second
			mu.Unlock()
			time.Sleep(wait)
			mu.Lock()
		}
		now := time.Now()
		lastOTP[l.Name] = now.Unix() / 30
		mu.Unlock()
		code, err := totp(seed, now)
		if err != nil {
			return 500, []byte("totp_seed: " + err.Error())
		}
		log.Printf("login %s: otp served", l.Name)
		b, _ := json.Marshal(map[string]string{"otp": code})
		return 200, b
	}))
	readData := func(cfg *Config, name string) (json.RawMessage, bool) {
		for _, s := range cfg.Sources {
			if s.Name == name && !strings.ContainsAny(name, `/\`) {
				b, err := os.ReadFile(filepath.Join(cfg.OutDir, name+".json"))
				return b, err == nil
			}
		}
		return nil, false
	}
	mux.HandleFunc("GET /data", guard(func(r *http.Request, cfg *Config, _ []byte) (int, []byte) {
		all := map[string]json.RawMessage{}
		for _, s := range cfg.Sources {
			if b, ok := readData(cfg, s.Name); ok {
				all[s.Name] = b
			}
		}
		b, _ := json.Marshal(all)
		return 200, b
	}))
	mux.HandleFunc("GET /data/{name}", guard(func(r *http.Request, cfg *Config, _ []byte) (int, []byte) {
		if b, ok := readData(cfg, r.PathValue("name")); ok {
			return 200, b
		}
		return 404, []byte("unknown or never-synced source")
	}))
	mux.HandleFunc("POST /push", guard(func(r *http.Request, cfg *Config, body []byte) (int, []byte) {
		var p Push
		if err := json.Unmarshal(body, &p); err != nil {
			return 400, []byte(err.Error())
		}
		var src *Source
		for i := range cfg.Sources {
			if cfg.Sources[i].Name == p.Name {
				src = &cfg.Sources[i]
			}
		}
		if src == nil || strings.ContainsAny(p.Name, `/\`) {
			return 404, []byte("unknown source")
		}
		p.FetchedAt = time.Now()
		b, _ := json.MarshalIndent(p, "", "  ")
		if err := writeFile(filepath.Join(cfg.OutDir, p.Name+".json"), b); err != nil {
			return 500, []byte(err.Error())
		}
		for _, wr := range src.Write {
			kind, key, _ := strings.Cut(wr.From, ":")
			v, ok := map[string]map[string]string{"cookie": p.Cookies, "localStorage": p.LocalStorage}[kind][key]
			if !ok {
				continue
			}
			if err := writeFile(expand(wr.To), []byte(v)); err != nil {
				log.Printf("%s: write %s: %v", p.Name, wr.To, err)
			}
		}
		mu.Lock()
		pushes[p.Name] = lastPush{p.FetchedAt, len(p.Cookies), len(p.LocalStorage), p.Errors}
		mu.Unlock()
		log.Printf("%s: %d cookies, %d localStorage, %d errors", p.Name, len(p.Cookies), len(p.LocalStorage), len(p.Errors))
		return 204, nil
	}))
	return mux
}

func main() {
	cfgPath := flag.String("config", "~/.config/chrome-bridge/config.yaml", "YAML config")
	flag.Parse()
	path := expand(*cfgPath)
	cfg, err := load(path)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("chrome-bridge on %s, %d sources, config %s", cfg.Listen, len(cfg.Sources), path)
	log.Fatal(http.ListenAndServe(cfg.Listen, handler(path)))
}
