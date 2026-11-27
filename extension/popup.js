import { call, settings, DEFAULT_BRIDGE, BridgeDown, AuthError } from "./bridge.js";

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
    return { status: JSON.parse(await call("/status")) };
  } catch (e) {
    if (e instanceof BridgeDown) return { down: true };
    if (e instanceof AuthError) return { auth: e.message };
    return { error: e.message };
  }
}

function renderServer({ status, down, error, auth }) {
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
  if (auth) {
    pill.className = "pill bad"; pill.textContent = "Auth";
    server.append(h("div", { className: "err", textContent: auth }));
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
  const v = status.vpn;
  if (v && !v.since.startsWith("0001")) {
    const tried = v.last_attempt.startsWith("0001") ? "" : `, reconnect tried ${ago(v.last_attempt)}`;
    server.append(h("div", { className: v.up ? "meta" : "err", textContent: `VPN ${v.up ? "up" : "down"} since ${ago(v.since)}${v.up ? "" : tried}` }));
    if (!v.up && v.last_error) server.append(h("div", { className: "err", textContent: v.last_error }));
  }
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

async function renderSettings() {
  const { base, secret } = await settings();
  BRIDGE = base;
  $("url").value = base;
  $("url-warn").hidden = isLoopback(base);
  $("secret").value = "";
  $("secret").placeholder = secret ? "Secret saved (type to replace)" : "Shared secret from config.yaml";
}

// Empty secret field keeps the saved one.
async function save(rawURL, rawSecret) {
  const msg = $("url-msg");
  let u;
  try {
    u = new URL(rawURL.trim());
    if (!/^https?:$/.test(u.protocol)) throw 0;
  } catch {
    msg.textContent = "Enter an http(s) URL, e.g. " + DEFAULT_BRIDGE;
    return;
  }
  const update = { bridge: u.origin };
  if (rawSecret) update.secret = rawSecret;
  await chrome.storage.local.set(update);
  msg.textContent = "Saved.";
  await renderSettings();
  render();
  if (rawSecret) chrome.runtime.sendMessage("sync");
}

async function render() {
  const res = await fetchStatus();
  const { sources = [], state = {} } = await chrome.storage.local.get(["sources", "state"]);
  renderServer(res);
  renderSites(res.status?.sources || sources, res.status?.pushes || {}, state);
  $("updated").textContent = "Updated " + new Date().toLocaleTimeString();
}

$("settings").addEventListener("submit", (e) => (e.preventDefault(), save($("url").value, $("secret").value)));
$("reset").addEventListener("click", () => save(DEFAULT_BRIDGE, ""));

$("sync").addEventListener("click", async () => {
  const b = $("sync");
  b.disabled = true; b.textContent = "Syncing…";
  await chrome.runtime.sendMessage("sync");
  await render();
  b.disabled = false; b.textContent = "Sync now";
});

renderSettings().then(async () => {
  // First run: open Settings so the secret can be entered.
  if (!(await settings()).secret) $("settings-box").open = true;
  render();
  setInterval(render, 5000);
});
