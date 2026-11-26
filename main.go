package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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

func handler(cfgPath string) http.Handler {
	mux := http.NewServeMux()
	guard := func(next func(http.ResponseWriter, *http.Request, *Config)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			cfg, err := load(cfgPath)
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			// Web pages can't forge a chrome-extension:// Origin, so sites you browse can't read or plant data.
			want := "chrome-extension://" + cfg.ExtensionID
			if o := r.Header.Get("Origin"); !strings.HasPrefix(o, "chrome-extension://") || (cfg.ExtensionID != "" && o != want) {
				http.Error(w, "forbidden", 403)
				return
			}
			next(w, r, cfg)
		}
	}
	mux.HandleFunc("GET /config", guard(func(w http.ResponseWriter, r *http.Request, cfg *Config) {
		json.NewEncoder(w).Encode(cfg.Sources)
	}))
	mux.HandleFunc("POST /push", guard(func(w http.ResponseWriter, r *http.Request, cfg *Config) {
		var p Push
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 5<<20)).Decode(&p); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		var src *Source
		for i := range cfg.Sources {
			if cfg.Sources[i].Name == p.Name {
				src = &cfg.Sources[i]
			}
		}
		if src == nil || strings.ContainsAny(p.Name, `/\`) {
			http.Error(w, "unknown source", 404)
			return
		}
		p.FetchedAt = time.Now()
		b, _ := json.MarshalIndent(p, "", "  ")
		if err := writeFile(filepath.Join(cfg.OutDir, p.Name+".json"), b); err != nil {
			http.Error(w, err.Error(), 500)
			return
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
		log.Printf("%s: %d cookies, %d localStorage, %d errors", p.Name, len(p.Cookies), len(p.LocalStorage), len(p.Errors))
		w.WriteHeader(204)
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
