# chrome-bridge

Pipes cookies and localStorage from the user's Chrome to local files, so scripts and skills can use browser sessions (first user: the `metabase-query` skill, via `metabase.SESSION`).

## Layout

- `main.go`: Go server. Reads the YAML on every request. `GET /config` (sources for the extension), `POST /push` (writes `<out_dir>/<name>.json` plus any `write:` targets, all 0600), `GET /status` (uptime, paths, last push per source: counts and errors only, never values), `GET /data` and `GET /data/{name}` (synced JSON for local workers; signed like every other endpoint, names limited to configured sources).
- `main_test.go`: one test: origin check, wrong secret, stale timestamp, response signatures, config, unknown source, push, write targets, and that `/status` leaks no values or secret.
- `extension/bridge.js`: the only code that talks to the server: signs requests, verifies response signatures, raises `BridgeDown` / `AuthError`. Used by background and popup (both ES modules).
- `extension/background.js`: every 30s alarm fetches `/config`, collects due sources, POSTs each; pushes immediately when a watched cookie changes. Sets the badge (`DOWN` red, `KEY` red for auth, `ERR` orange) and keeps per-source state in `chrome.storage.local` for the popup.
- `extension/popup.{html,js}`: toolbar popup, plain HTML/CSS/JS (no framework, no build). Polls `/status` every 5s; shows server live/down, address, uptime, paths, and each site's last sync, cookie counts and errors. "Sync now" messages the background to force a full sync.
- `config.example.yaml`: documented config. The live config is `config.yaml` in this folder (gitignored, holds personal sites); the LaunchAgent passes `-config` pointing at it. Output data stays in `~/.config/chrome-bridge/data`.

## Commands

```bash
go test ./...                      # must pass before any change is done
go build -o ~/.local/bin/chrome-bridge .   # the binary launchd runs
~/.local/bin/chrome-bridge -config config.yaml   # manual run (default without -config: ~/.config/chrome-bridge/config.yaml)
curl -s 127.0.0.1:47123/config   # → 401 when the server is up (all requests must be signed)
```

After editing `extension/`, reload it in `chrome://extensions`. YAML edits need no restart or reload.

## Invariants (don't break)

- Listen on `127.0.0.1` only. Port 47123 is the default in `listen` and in `DEFAULT_BRIDGE` (`extension/background.js`, `extension/popup.js`). Users can point the extension elsewhere from the popup's Settings (stored as `bridge` in `chrome.storage.local`).
- Auth is a shared `secret` (in `config.yaml`, entered once in the popup's Settings). It never crosses the wire:
  - requests carry `X-Chrome-Bridge-Time` (unix seconds, ±60s) and `X-Chrome-Bridge-Sig` = hex HMAC-SHA256(secret, `time\nMETHOD\npath\nbody`); bad or stale → 401.
  - responses carry `X-Chrome-Bridge-Sig` = HMAC(secret, `resp\ntime\nbody`); `extension/bridge.js` rejects unsigned responses, so a process squatting on the port can't hand the extension a config (e.g. `cookies: ["*"]` for any site).
  - `Origin`, when present (Chrome omits it on extension GETs), must be `chrome-extension://`, and equal `extension_id` when set → else 403.
  - Signing lives only in `sign()` (main.go) and `bridge.js`; change both together. Never send the secret itself, never add CORS allow headers, never add an endpoint outside `guard`.
- Source names must not contain path separators; they become file names.
- Never log or print cookie/localStorage values. Log counts only.
- Push bodies are capped at 5 MB.

## Known limits

- localStorage is read only from an open tab of the source's origin; otherwise it's skipped with an entry in `errors`.
- Chrome alarms fire at most every 30s, so `every` < 30s acts as 30s.
- Extension has `<all_urls>` host permission so new YAML sources work without a manifest change; it only collects sources listed in the config.

## Running (launchd)

The server runs as LaunchAgent `com.yashkumarverma.chrome-bridge` (`~/Library/LaunchAgents/com.yashkumarverma.chrome-bridge.plist`), starting at login and restarting if it dies. It runs `~/.local/bin/chrome-bridge`; logs go to `~/Library/Logs/chrome-bridge.log`.

Never point launchd at a binary under `~/Desktop`: macOS's Desktop privacy protection blocks a launchd job from opening it (dyld hangs in `open`, no log line, port never binds), and every rebuild counts as a new app.

```bash
go build -o ~/.local/bin/chrome-bridge . && launchctl kickstart -k gui/$(id -u)/com.yashkumarverma.chrome-bridge   # deploy a rebuild
launchctl print gui/$(id -u)/com.yashkumarverma.chrome-bridge | grep -E 'state|pid'              # status
tail -f ~/Library/Logs/chrome-bridge.log
```

Don't also start the binary by hand while the agent runs: port 47123 will already be taken.

## Style

Stdlib plus `gopkg.in/yaml.v3` only. Keep it to these few files; no new dependencies or packages without a reason.
