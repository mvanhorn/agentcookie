// agentcookie extension service worker.
//
// Long-polls the local agentcookie sink for pending cookie batches. For each
// batch, calls chrome.cookies.set() per cookie (this is the load-bearing
// switch from CDP Storage.setCookies, which silently drops valid cookies).
// Posts per-cookie success/error back to the sink.
//
// Trust model: the sink is reachable only on 127.0.0.1, the channel is
// authed by a per-install shared secret (X-AgentCookie-Token header) that
// the wizard installer writes into chrome.storage.local at install time.

const DEFAULT_SINK_URL = "http://127.0.0.1:9999";

// Convert a Chrome SQLite WebKit microsecond timestamp to Unix seconds.
const CHROME_EPOCH_DELTA_SECONDS = 11644473600;

async function getConfig() {
  const stored = await chrome.storage.local.get(["sinkURL", "token"]);
  return {
    sinkURL: stored.sinkURL || DEFAULT_SINK_URL,
    token: stored.token || "agentcookie-default-token",
  };
}

// toSetParams translates a sink-side chrome.Cookie record to the
// chrome.cookies.set() argument shape. Critical: chrome.cookies.set requires
// the `url` parameter (it cannot use bare domain+path). For host-only cookies
// we set domain to empty and let Chrome derive from url; for share-across-
// subdomain cookies (leading dot in host_key), we set domain explicitly.
function toSetParams(c) {
  const hostKey = c.host_key || "";
  const isSubdomainWildcard = hostKey.startsWith(".");
  const host = isSubdomainWildcard ? hostKey.substring(1) : hostKey;
  const scheme = c.is_secure ? "https" : "http";
  const path = c.path || "/";
  const url = `${scheme}://${host}${path}`;

  const params = {
    url: url,
    name: c.name,
    value: c.value,
    path: path,
    secure: !!c.is_secure,
    httpOnly: !!c.is_httponly,
  };

  if (isSubdomainWildcard) {
    params.domain = hostKey;
  }

  // sameSite: 0=None, 1=Lax, 2=Strict, -1=Unspecified
  switch (c.samesite) {
    case 0:
      if (c.is_secure) {
        params.sameSite = "no_restriction";
      }
      // SameSite=None without Secure: omit (Chrome rejects the combination).
      break;
    case 1:
      params.sameSite = "lax";
      break;
    case 2:
      params.sameSite = "strict";
      break;
    default:
      params.sameSite = "unspecified";
  }

  if (c.has_expires && c.expires_utc > 0) {
    params.expirationDate = c.expires_utc / 1e6 - CHROME_EPOCH_DELTA_SECONDS;
  }

  return params;
}

async function setOneCookie(c) {
  const params = toSetParams(c);
  try {
    const result = await chrome.cookies.set(params);
    if (result === null) {
      return { name: c.name, host: c.host_key, success: false, error: "chrome.cookies.set returned null" };
    }
    return { name: c.name, host: c.host_key, success: true };
  } catch (e) {
    return { name: c.name, host: c.host_key, success: false, error: String(e && e.message ? e.message : e) };
  }
}

async function processBatch(batch, cfg) {
  if (!batch || !Array.isArray(batch.cookies) || batch.cookies.length === 0) {
    return;
  }
  const results = [];
  // Process in chunks of 100 to avoid pathologically large
  // chrome.storage.local writes if Chrome buffers internally.
  for (let i = 0; i < batch.cookies.length; i += 100) {
    const slice = batch.cookies.slice(i, i + 100);
    const chunk = await Promise.all(slice.map(setOneCookie));
    results.push(...chunk);
  }
  await fetch(`${cfg.sinkURL}/extension/cookies/result`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      "X-AgentCookie-Token": cfg.token,
    },
    body: JSON.stringify({ batch_id: batch.batch_id, results }),
  }).catch(() => {});
}

async function longPollOnce(cfg) {
  let resp;
  try {
    resp = await fetch(`${cfg.sinkURL}/extension/cookies/pending`, {
      method: "GET",
      headers: { "X-AgentCookie-Token": cfg.token },
    });
  } catch {
    await new Promise((r) => setTimeout(r, 1000));
    return;
  }
  if (resp.status === 204) {
    // No pending work; long-poll returned empty.
    return;
  }
  if (!resp.ok) {
    await new Promise((r) => setTimeout(r, 2000));
    return;
  }
  let batch;
  try {
    batch = await resp.json();
  } catch {
    return;
  }
  await processBatch(batch, cfg);
}

async function pollLoop() {
  while (true) {
    const cfg = await getConfig();
    await longPollOnce(cfg);
  }
}

// Service workers can be evicted; restart the loop on every startup event.
self.addEventListener("activate", () => {
  pollLoop();
});

pollLoop();
