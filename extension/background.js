const BRIDGE = "http://127.0.0.1:47123";

async function collect(s) {
  const out = { name: s.name, cookies: {}, local_storage: {}, errors: [] };
  const all = (s.cookies || []).includes("*");
  const cookies = all
    ? await chrome.cookies.getAll({ url: s.url })
    : (await Promise.all((s.cookies || []).map((name) => chrome.cookies.get({ url: s.url, name })))).filter(Boolean);
  for (const c of cookies) out.cookies[c.name] = c.value;

  const keys = s.local_storage || [];
  if (keys.length) {
    // localStorage is only reachable through an open tab of that origin.
    const [tab] = await chrome.tabs.query({ url: new URL(s.url).origin + "/*" });
    if (!tab) out.errors.push("localStorage: no open tab for " + s.url);
    else {
      const [{ result }] = await chrome.scripting.executeScript({
        target: { tabId: tab.id },
        args: [keys],
        func: (keys) =>
          keys.includes("*")
            ? { ...localStorage }
            : Object.fromEntries(keys.filter((k) => localStorage.getItem(k) !== null).map((k) => [k, localStorage.getItem(k)])),
      });
      out.local_storage = result;
    }
  }
  const r = await fetch(BRIDGE + "/push", { method: "POST", body: JSON.stringify(out) });
  if (!r.ok) throw new Error(s.name + ": push " + r.status);
}

async function tick(force = false) {
  try {
    const sources = await (await fetch(BRIDGE + "/config")).json();
    const { last = {} } = await chrome.storage.local.get("last");
    await chrome.storage.local.set({ sources });
    const now = Date.now();
    for (const s of sources) {
      if (!force && now - (last[s.name] || 0) < s.every_seconds * 1000) continue;
      await collect(s);
      last[s.name] = now;
    }
    await chrome.storage.local.set({ last });
    chrome.action.setBadgeText({ text: "" });
  } catch (e) {
    console.error(e);
    chrome.action.setBadgeText({ text: "err" });
  }
}

// ponytail: Chrome alarms fire at most every 30s, so `every` below 30s behaves as 30s.
const start = () => chrome.alarms.create("tick", { periodInMinutes: 0.5 });
chrome.runtime.onInstalled.addListener(() => (start(), tick(true)));
chrome.runtime.onStartup.addListener(() => (start(), tick(true)));
chrome.alarms.onAlarm.addListener(() => tick());
chrome.action.onClicked.addListener(() => tick(true));

// A configured cookie changed (e.g. fresh login): push that source now instead of waiting.
chrome.cookies.onChanged.addListener(async ({ cookie, removed }) => {
  if (removed) return;
  const { sources = [] } = await chrome.storage.local.get("sources");
  for (const s of sources) {
    const host = new URL(s.url).hostname;
    const watched = (s.cookies || []).includes("*") || (s.cookies || []).includes(cookie.name);
    if (watched && host.endsWith(cookie.domain.replace(/^\./, ""))) collect(s).catch(console.error);
  }
});
