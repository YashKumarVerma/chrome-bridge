import { call, BridgeDown, AuthError } from "./bridge.js";

const FIX_DOWN = "Start it: launchctl kickstart -k gui/$(id -u)/com.yashkumarverma.chrome-bridge (log: ~/Library/Logs/chrome-bridge.log)";

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
  const auth = problems.find((p) => p instanceof AuthError);
  if (auth) {
    console.warn(auth.message);
    return status("KEY", "#d93025", auth.message);
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
    // Only a verified (signed) config is acted on, so a port squatter can't choose what gets collected.
    const sources = JSON.parse(await call("/config"));
    await chrome.storage.local.set({ logins: JSON.parse(await call("/logins")) });
    const { last = {} } = await chrome.storage.local.get("last");
    await chrome.storage.local.set({ sources });
    const now = Date.now();
    for (const s of sources) {
      if (!force && now - (last[s.name] || 0) < s.every_seconds * 1000) continue;
      try {
        problems.push(...(await collect(s)));
        last[s.name] = now;
      } catch (e) {
        if (e instanceof BridgeDown || e instanceof AuthError) throw e;
        await saveState(s.name, { at: Date.now(), errors: [e.message] });
        problems.push(new Error(`${s.name}: ${e.message}`));
      }
    }
    await chrome.storage.local.set({ last });
    const { loginBlocked = {} } = await chrome.storage.local.get("loginBlocked");
    for (const n in loginBlocked) problems.push(new Error(`${n}: auto-login failed and is paused; log in by hand, then Sync now`));
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
  if (msg === "sync") chrome.storage.local.set({ loginBlocked: {} }).then(() => tick(true)).then(() => reply(true));
  return true;
});

// A configured cookie changed (e.g. fresh login): push that source now instead of waiting.
// `sources` in storage only ever comes from a verified /config.
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

// --- Auto-login (logins: in config.yaml) ---
// Runs in the login tab's isolated world: fills whichever step is on screen, once per step.
// Credentials come from the background via messages, never stored in the page.
function loginFiller(name) {
  if (window.__cbLogin) return;
  window.__cbLogin = true;
  const done = new Set();
  const visible = (sel) => [...document.querySelectorAll(sel)].find((e) => e.offsetParent !== null);
  const ask = (want) => chrome.runtime.sendMessage({ login: name, want });
  const set = (el, v) => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value").set.call(el, v);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  };
  const click = (sel) => setTimeout(() => document.querySelector(sel)?.click(), 300);
  const step = async () => {
    if (!done.has("otp") && document.querySelector('input[name="2fa"]')) {
      done.add("otp");
      const { otp } = await ask("otp");
      set(document.querySelector('input[name="2fa"]'), otp);
      setTimeout(() => document.querySelector('input[name="2fa"]') && ask("failed"), 20000);
    } else if (!done.has("mfa") && document.querySelector('[data-test-id="UserLogin__MfaChooser__MfaButtons__totp"]')) {
      done.add("mfa");
      click('[data-test-id="UserLogin__MfaChooser__MfaButtons__totp"]');
    } else if (!done.has("password") && visible('input[name="password"]')) {
      done.add("password");
      const { password } = await ask("creds");
      set(visible('input[name="password"]'), password);
      click('[data-automation="loginButton"]');
    } else if (!done.has("email") && visible('input[name="email"]')) {
      done.add("email");
      const { email } = await ask("creds");
      set(visible('input[name="email"]'), email);
      click('[data-automation="loginButton"]');
    }
  };
  const t = setInterval(() => step().catch(() => {}), 700);
  setTimeout(() => clearInterval(t), 180000);
}

const loginFor = async (url) => {
  const { logins = [], loginBlocked = {} } = await chrome.storage.local.get(["logins", "loginBlocked"]);
  return logins.find((l) => url?.startsWith(l.match.replace(/\*$/, "")) && !loginBlocked[l.name]);
};

chrome.tabs.onUpdated.addListener(async (tabId, info, tab) => {
  if (info.status !== "complete" && !info.url) return;
  const l = await loginFor(tab.url);
  if (l) chrome.scripting.executeScript({ target: { tabId }, func: loginFiller, args: [l.name] }).catch((e) => console.warn(e));
});

// Only our injected filler can message us (no externally_connectable); still require the sender tab to be on the login URL.
chrome.runtime.onMessage.addListener((msg, sender, reply) => {
  if (!msg?.login) return;
  (async () => {
    const l = await loginFor(sender.tab?.url);
    if (!l || l.name !== msg.login) return reply({});
    if (msg.want === "failed") {
      // Stop auto-filling until "Sync now", so a wrong password can't lock the account.
      const { loginBlocked = {} } = await chrome.storage.local.get("loginBlocked");
      await chrome.storage.local.set({ loginBlocked: { ...loginBlocked, [l.name]: Date.now() } });
      status("AUTH", "#d93025", `${l.name}: auto-login failed; paused. Log in by hand, then click Sync now to re-enable.`);
      return reply({});
    }
    try {
      reply(JSON.parse(await call(`/login/${l.name}${msg.want === "otp" ? "/otp" : ""}`, { method: "POST" })));
    } catch (e) {
      report([e]);
      reply({});
    }
  })();
  return true;
});
