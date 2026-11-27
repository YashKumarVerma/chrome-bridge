package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
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

type Config struct {
	Listen      string   `yaml:"listen"`
	OutDir      string   `yaml:"out_dir"`
	ExtensionID string   `yaml:"extension_id"`
	Secret      string   `yaml:"secret"`
	Sources     []Source `yaml:"sources"`
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
			"sources": cfg.Sources, "pushes": pushes,
		})
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
