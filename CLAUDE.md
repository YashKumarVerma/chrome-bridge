# chrome-bridge

Pipes cookies and localStorage from the user's Chrome to local files, so scripts and skills can use browser sessions (first user: the `metabase-query` skill, via `metabase.SESSION`).

## Layout

- `main.go`: Go server. Reads the YAML on every request, serves `GET /config` to the extension, accepts `POST /push`, writes `<out_dir>/<name>.json` plus any `write:` targets (all 0600).
- `main_test.go`: one test covering origin check, config, unknown source, push and write targets.
- `extension/`: MV3 extension. Every 30s alarm: fetch `/config`, collect due sources, POST each. Also pushes immediately when a watched cookie changes, and on toolbar click (forces all).
- `config.example.yaml`: documented config; live copy at `~/.config/chrome-bridge/config.yaml`.

## Commands

```bash
go test ./...                      # must pass before any change is done
go build -o chrome-bridge .        # rebuild after any Go edit
./chrome-bridge [-config path]     # default ~/.config/chrome-bridge/config.yaml
curl -s -H 'Origin: chrome-extension://x' 127.0.0.1:47123/config   # inspect what the extension sees
```

After editing `extension/`, reload it in `chrome://extensions`. YAML edits need no restart or reload.

## Invariants (don't break)

- Listen on `127.0.0.1` only. Port 47123 is hardcoded in `extension/background.js`; change both together.
- Every endpoint requires an `Origin: chrome-extension://...` header (pinned to `extension_id` when set). This is what stops web pages from reading or planting data. Never add CORS headers or an endpoint without the guard.
- Source names must not contain path separators; they become file names.
- Never log or print cookie/localStorage values. Log counts only.
- Push bodies are capped at 5 MB.

## Known limits

- localStorage is read only from an open tab of the source's origin; otherwise it's skipped with an entry in `errors`.
- Chrome alarms fire at most every 30s, so `every` < 30s acts as 30s.
- Extension has `<all_urls>` host permission so new YAML sources work without a manifest change; it only collects sources listed in the config.
- Server runs only while its terminal is open; no launchd agent yet.

## Style

Stdlib plus `gopkg.in/yaml.v3` only. Keep it to these few files; no new dependencies or packages without a reason.
