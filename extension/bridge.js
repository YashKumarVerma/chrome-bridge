export const DEFAULT_BRIDGE = "http://127.0.0.1:47123";

export class BridgeDown extends Error {}
export class AuthError extends Error {}

const enc = new TextEncoder();

async function sign(secret, msg) {
  const key = await crypto.subtle.importKey("raw", enc.encode(secret), { name: "HMAC", hash: "SHA-256" }, false, ["sign"]);
  const mac = await crypto.subtle.sign("HMAC", key, enc.encode(msg));
  return [...new Uint8Array(mac)].map((b) => b.toString(16).padStart(2, "0")).join("");
}

export async function settings() {
  const { bridge, secret } = await chrome.storage.local.get(["bridge", "secret"]);
  return { base: bridge || DEFAULT_BRIDGE, secret: secret || "" };
}

// Signs the request and verifies the response signature; the secret itself is never sent.
// Returns the response body as text.
export async function call(path, { method = "GET", body = "" } = {}) {
  const { base, secret } = await settings();
  if (!secret) throw new AuthError("No shared secret set: open the popup → Settings.");
  const ts = String(Math.floor(Date.now() / 1000));
  const headers = {
    "X-Chrome-Bridge-Time": ts,
    "X-Chrome-Bridge-Sig": await sign(secret, `${ts}\n${method}\n${path}\n${body}`),
  };
  let r;
  try {
    r = await fetch(base + path, { method, headers, body: method === "GET" ? undefined : body });
  } catch {
    throw new BridgeDown(`chrome-bridge server not reachable at ${base}`);
  }
  const text = await r.text();
  if (r.status === 401) throw new AuthError(`Server rejected the request: ${text.trim()}. Check the secret in Settings matches config.yaml.`);
  if (!r.ok) throw new Error(`${path}: HTTP ${r.status} ${text.trim()}`);
  if (r.headers.get("X-Chrome-Bridge-Sig") !== (await sign(secret, `resp\n${ts}\n${text}`))) {
    throw new AuthError(`Response from ${base} isn't signed with your secret: not your chrome-bridge server, or the secret differs.`);
  }
  return text;
}
