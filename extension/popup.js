const DEFAULT_BRIDGE = "http://127.0.0.1:47123";
let BRIDGE = DEFAULT_BRIDGE;
const FIX = "launchctl kickstart -k gui/$(id -u)/com.yashkumarverma.chrome-bridge";
const $ = (id) => document.getElementById(id);

function h(tag, attrs = {}, ...kids) {
  const el = document.createElement(tag);
  Object.assign(el, attrs);
  el.append(...kids.filter((k) => k != null && k !== false));
  return el;
}

function ago(t) {
  if (!t) return "never";
  const s = Math.round((Date.now() - new Date(t)) / 1000);
  if (s < 60) return s + "s ago";
  if (s < 3600) return Math.round(s / 60) + "m ago";
  if (s < 86400) return Math.round(s / 3600) + "h ago";
  return Math.round(s / 86400) + "d ago";
}

function every(sec) {
  return sec % 3600 === 0 ? sec / 3600 + "h" : sec % 60 === 0 ? sec / 60 + "m" : sec + "s";
}

async function fetchStatus() {
  try {
    const r = await fetch(BRIDGE + "/status", { headers: { "X-Chrome-Bridge": "1" } });
    if (!r.ok) return { error: `HTTP ${r.status}: ${(await r.text()).trim()}` };
    return { status: await r.json() };
  } catch {
    return { down: true };
  }
}

function renderServer({ status, down, error }) {
  const pill = $("pill");
  const server = $("server");
  server.replaceChildren(h("h2", { textContent: "Server" }));
  if (down) {
    pill.className = "pill bad"; pill.textContent = "Down";
    server.append(
      h("div", { textContent: `Not reachable at ${BRIDGE}. Start it:` }),
      h("code", { textContent: FIX }),
      h("div", { className: "meta", textContent: "Log: ~/Library/Logs/chrome-bridge.log" }),
    );
    return;
  }
  if (error) {
    pill.className = "pill warn"; pill.textContent = "Error";
    server.append(h("div", { className: "err", textContent: error }));
    return;
  }
  pill.className = "pill ok"; pill.textContent = "Live";
  server.append(h("dl", {},
    h("dt", { textContent: "Address" }), h("dd", { textContent: status.listen }),
    h("dt", { textContent: "Up since" }), h("dd", { textContent: `${new Date(status.started_at).toLocaleString()} (${ago(status.started_at)})` }),
    h("dt", { textContent: "Config" }), h("dd", { textContent: status.config, title: status.config }),
    h("dt", { textContent: "Output" }), h("dd", { textContent: status.out_dir, title: status.out_dir }),
  ));
}

function renderSites(sources, pushes, local) {
  const box = $("sites");
  if (!sources.length) return box.replaceChildren(h("div", { className: "empty", textContent: "No sources in config." }));
  box.replaceChildren(...sources.map((s) => {
    const st = local[s.name] || {};
    const push = pushes[s.name];
    const errors = st.errors || push?.errors || [];
    const synced = push?.at || st.at;
    const [cls, label] = !synced ? ["idle", "Pending"] : errors.length ? ["warn", "Issue"] : ["ok", "Synced"];
    const wantCookies = (s.cookies || []).includes("*") ? "all" : (s.cookies || []).length;
    const gotCookies = st.cookies?.length ?? push?.cookies ?? 0;
    const parts = [`every ${every(s.every_seconds)}`, `synced ${ago(synced)}`, `${gotCookies}/${wantCookies} cookies`];
    if ((s.local_storage || []).length) parts.push(`${st.local_storage?.length ?? push?.local_storage ?? 0} localStorage`);
    return h("div", { className: "site" },
      h("div", { className: "row" },
        h("span", { className: "name", textContent: s.name }),
        h("span", { className: "pill " + cls, textContent: label })),
      h("div", { className: "meta", textContent: new URL(s.url).host }),
      h("div", { className: "meta", textContent: parts.join(" · ") }),
      ...errors.map((e) => h("div", { className: "err", textContent: e })),
    );
  }));
}

function isLoopback(u) {
  return ["127.0.0.1", "localhost", "[::1]"].includes(new URL(u).hostname);
}

function renderSettings() {
  $("url").value = BRIDGE;
  $("url-warn").hidden = isLoopback(BRIDGE);
}

async function saveURL(raw) {
  const msg = $("url-msg");
  let u;
  try {
    u = new URL(raw.trim());
    if (!/^https?:$/.test(u.protocol)) throw 0;
  } catch {
    msg.textContent = "Enter an http(s) URL, e.g. " + DEFAULT_BRIDGE;
    return;
  }
  BRIDGE = u.origin;
  await chrome.storage.local.set({ bridge: BRIDGE });
  msg.textContent = "Saved.";
  renderSettings();
  render();
}

async function render() {
  const res = await fetchStatus();
  const { sources = [], state = {} } = await chrome.storage.local.get(["sources", "state"]);
  renderServer(res);
  renderSites(res.status?.sources || sources, res.status?.pushes || {}, state);
  $("updated").textContent = "Updated " + new Date().toLocaleTimeString();
}

$("settings").addEventListener("submit", (e) => (e.preventDefault(), saveURL($("url").value)));
$("reset").addEventListener("click", () => saveURL(DEFAULT_BRIDGE));

$("sync").addEventListener("click", async () => {
  const b = $("sync");
  b.disabled = true; b.textContent = "Syncing…";
  await chrome.runtime.sendMessage("sync");
  await render();
  b.disabled = false; b.textContent = "Sync now";
});

chrome.storage.local.get("bridge").then(({ bridge }) => {
  BRIDGE = bridge || DEFAULT_BRIDGE;
  renderSettings();
  render();
  setInterval(render, 5000);
});
