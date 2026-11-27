const DEFAULT_BRIDGE = "http://127.0.0.1:47123";
const FIX_DOWN = "Start it: launchctl kickstart -k gui/$(id -u)/com.yashkumarverma.chrome-bridge (log: ~/Library/Logs/chrome-bridge.log)";

// Server URL is set from the popup; stored without a trailing slash.
const bridgeURL = async () => (await chrome.storage.local.get("bridge")).bridge || DEFAULT_BRIDGE;

class BridgeDown extends Error {}

async function call(path, opts) {
  const base = await bridgeURL();
  let r;
  try {
    r = await fetch(base + path, { ...opts, headers: { "X-Chrome-Bridge": "1" } });
  } catch {
    throw new BridgeDown(`chrome-bridge server not reachable at ${base}`);
  }
  if (!r.ok) throw new Error(`${path}: HTTP ${r.status} ${(await r.text()).trim()}`);
  return r;
}

function status(badge, color, title) {
  chrome.action.setBadgeText({ text: badge });
  if (badge) chrome.action.setBadgeBackgroundColor({ color });
  chrome.action.setTitle({ title: "Chrome Bridge\n" + title });
}

function report(problems) {
  const down = problems.find((p) => p instanceof BridgeDown);
  if (down) {
    console.warn(down.message);
    return status("DOWN", "#d93025", `${down.message}\n${FIX_DOWN}`);
  }
  const msgs = problems.map((p) => p.message || String(p));
  if (msgs.length) {
    console.warn(msgs.join("\n"));
    return status("ERR", "#e37400", msgs.join("\n"));
  }
  status("", "", "Last sync " + new Date().toLocaleTimeString());
}

// Returns soft problems (e.g. no tab for localStorage); throws on push failure.
async function collect(s) {
  const out = { name: s.name, cookies: {}, local_storage: {}, errors: [] };
  const all = (s.cookies || []).includes("*");
  const cookies = all
    ? await chrome.cookies.getAll({ url: s.url })
    : (await Promise.all((s.cookies || []).map((name) => chrome.cookies.get({ url: s.url, name })))).filter(Boolean);
  for (const c of cookies) out.cookies[c.name] = c.value;
  const missing = all ? [] : (s.cookies || []).filter((n) => !(n in out.cookies));
  if (missing.length) out.errors.push(`cookies not found (logged out?): ${missing.join(", ")}`);

  const keys = s.local_storage || [];
  if (keys.length) {
    // localStorage is only reachable through an open tab of that origin.
    const [tab] = await chrome.tabs.query({ url: new URL(s.url).origin + "/*" });
    if (!tab) out.errors.push("localStorage: no open tab for " + s.url);
    else {
      try {
        const [{ result }] = await chrome.scripting.executeScript({
          target: { tabId: tab.id },
          args: [keys],
          func: (keys) =>
            keys.includes("*")
              ? { ...localStorage }
              : Object.fromEntries(keys.filter((k) => localStorage.getItem(k) !== null).map((k) => [k, localStorage.getItem(k)])),
        });
        out.local_storage = result;
      } catch (e) {
        out.errors.push("localStorage: " + e.message);
      }
    }
  }
  await call("/push", { method: "POST", body: JSON.stringify(out) });
  await saveState(s.name, { at: Date.now(), cookies: Object.keys(out.cookies), local_storage: Object.keys(out.local_storage), errors: out.errors });
  return out.errors.map((e) => new Error(`${s.name}: ${e}`));
}

async function saveState(name, st) {
  const { state = {} } = await chrome.storage.local.get("state");
  state[name] = st;
  await chrome.storage.local.set({ state });
}

async function tick(force = false) {
  const problems = [];
  try {
    const sources = await (await call("/config")).json();
    const { last = {} } = await chrome.storage.local.get("last");
    await chrome.storage.local.set({ sources });
    const now = Date.now();
    for (const s of sources) {
      if (!force && now - (last[s.name] || 0) < s.every_seconds * 1000) continue;
      try {
        problems.push(...(await collect(s)));
        last[s.name] = now;
      } catch (e) {
        if (e instanceof BridgeDown) throw e;
        await saveState(s.name, { at: Date.now(), errors: [e.message] });
        problems.push(new Error(`${s.name}: ${e.message}`));
      }
    }
    await chrome.storage.local.set({ last });
  } catch (e) {
    problems.push(e);
  }
  report(problems);
}

// ponytail: Chrome alarms fire at most every 30s, so `every` below 30s behaves as 30s.
const start = () => chrome.alarms.create("tick", { periodInMinutes: 0.5 });
chrome.runtime.onInstalled.addListener(() => (start(), tick(true)));
chrome.runtime.onStartup.addListener(() => (start(), tick(true)));
chrome.alarms.onAlarm.addListener(() => tick());
// Popup's "Sync now" button.
chrome.runtime.onMessage.addListener((msg, _, reply) => {
  if (msg === "sync") tick(true).then(() => reply(true));
  return true;
});

// A configured cookie changed (e.g. fresh login): push that source now instead of waiting.
chrome.cookies.onChanged.addListener(async ({ cookie, removed }) => {
  if (removed) return;
  const { sources = [] } = await chrome.storage.local.get("sources");
  for (const s of sources) {
    const host = new URL(s.url).hostname;
    const watched = (s.cookies || []).includes("*") || (s.cookies || []).includes(cookie.name);
    if (watched && host.endsWith(cookie.domain.replace(/^\./, ""))) {
      collect(s).then(report, (e) => report([e]));
    }
  }
});
