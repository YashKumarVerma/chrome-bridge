# chrome-bridge

Sync cookies and localStorage from your Chrome into local files, on a schedule, so scripts and CLI tools can reuse your logged-in browser sessions.

A small Chrome extension collects what a YAML config asks for and POSTs it to a Go server on `127.0.0.1`, which writes it to disk.

```
Chrome extension ──every N── GET /config ──▶ chrome-bridge (Go, 127.0.0.1:47123)
                 ──────────── POST /push ──▶   ├─ ~/.config/chrome-bridge/data/<name>.json
                                               └─ optional per-value files (write:)
```

## Setup

```bash
go build -o chrome-bridge .
cp config.example.yaml config.yaml   # then edit, set `secret`; config.yaml is gitignored
./chrome-bridge -config config.yaml  # default without -config: ~/.config/chrome-bridge/config.yaml
```

In Chrome: `chrome://extensions` → enable **Developer mode** → **Load unpacked** → select `extension/`.
Click the extension icon for the status popup: server live/down, uptime, config paths, and each site's last sync, cookie counts and errors. **Sync now** forces a full sync. **Settings** holds the shared secret and the server URL (default `http://127.0.0.1:47123`).

## Config

```yaml
listen: 127.0.0.1:47123
out_dir: ~/.config/chrome-bridge/data
extension_id: ""                  # optional: accept only this extension's id
secret: "<long random string>"    # required; enter the same value in the popup's Settings

sources:
  - name: example                 # output file name: <out_dir>/example.json
    url: https://app.example.com
    every: 5m                     # Go duration; minimum effective 30s
    cookies: [session_id]         # names, or ["*"] for all cookies for the URL
    local_storage: [auth_token]   # keys, or ["*"]; needs an open tab of the site
    write:                        # optional: write single values to their own files
      - from: cookie:session_id   # cookie:<name> or localStorage:<key>
        to: ~/.config/example/session
```

The server re-reads the YAML on every request and the extension fetches it every 30s, so edits apply without restarting anything.

Output (`<out_dir>/<name>.json`, mode 0600):

```json
{
  "name": "example",
  "cookies": { "session_id": "..." },
  "local_storage": { "auth_token": "..." },
  "errors": [],
  "fetched_at": "2026-11-27T05:21:07+05:30"
}
```

## Behaviour

- Sources sync when their `every` interval elapses, when a watched cookie changes (e.g. you log in again), and when you click the extension icon.
- localStorage is only readable from an open tab of the source's origin. With no such tab, it's skipped and noted in `errors`.
- Chrome alarms fire at most every 30s, so shorter intervals behave as 30s.
- The toolbar badge shows status; hover it for details:
  - none: last sync worked (tooltip shows the time)
  - red `DOWN`: server not reachable on 127.0.0.1:47123 (tooltip shows how to start it)
  - red `KEY`: no secret set, the server rejected it, or the response wasn't signed with it
  - orange `ERR`: server rejected a push, a cookie is missing (logged out), or localStorage couldn't be read

## Run at login (macOS)

Build to a path outside `~/Desktop`, `~/Documents` and `~/Downloads` (macOS privacy protection blocks launchd jobs from opening binaries there), e.g. `go build -o ~/.local/bin/chrome-bridge .`. Create `~/Library/LaunchAgents/com.<you>.chrome-bridge.plist` with `ProgramArguments` pointing at it, plus `-config` and the absolute path of your `config.yaml`, `RunAtLoad` and `KeepAlive` true, then:

```bash
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.<you>.chrome-bridge.plist
launchctl kickstart -k gui/$(id -u)/com.<you>.chrome-bridge   # restart after a rebuild
```

## Security

- The server binds to `127.0.0.1` only.
- Requests and responses are signed with HMAC-SHA256 over a shared `secret` set in `config.yaml` and entered once in the popup's Settings. The secret itself never crosses the wire. The server rejects unsigned or stale (over 60s) requests, and the extension ignores responses that aren't signed with the secret, so another process squatting on the port can't direct what it collects.
- Any `Origin` present must be `chrome-extension://`, pinned to your install when `extension_id` is set. Rejections are logged with the reason.
- Files are written 0600. Values are never logged.
- The extension holds `<all_urls>` host permission so new sources work without editing the manifest. It only collects sources from a config signed with your secret. Anything that can read or write your `config.yaml` has the secret; keep it 0600.

## Development

```bash
go test ./...
```
