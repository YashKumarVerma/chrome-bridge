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
mkdir -p ~/.config/chrome-bridge
cp config.example.yaml ~/.config/chrome-bridge/config.yaml   # then edit
./chrome-bridge                                              # -config <path> to override
```

In Chrome: `chrome://extensions` → enable **Developer mode** → **Load unpacked** → select `extension/`.
Click the extension icon to sync immediately.

## Config

```yaml
listen: 127.0.0.1:47123
out_dir: ~/.config/chrome-bridge/data
extension_id: ""                  # optional: accept only this extension's id

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

## Security

- The server binds to `127.0.0.1` only.
- Every request must carry a `chrome-extension://` Origin, which web pages cannot forge; set `extension_id` to pin it to your install.
- Files are written 0600. Values are never logged.
- The extension holds `<all_urls>` host permission so new sources work without editing the manifest. It only collects sources listed in your config and only sends them to localhost. Anything that can write your config or bind the port first can request your cookies; treat both accordingly.

## Development

```bash
go test ./...
```
