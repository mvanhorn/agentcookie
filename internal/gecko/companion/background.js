/* The companion never receives tokens through page-world arguments or URLs. */
'use strict';
(() => {
  const cfg = globalThis.AgentCookieConfig;
  let connection, delay = 1000;
  const send = value => {
    if (!connection || connection.readyState !== WebSocket.OPEN) return;
    const text = JSON.stringify(value);
    if (new TextEncoder().encode(text).length > 32 * 1024 * 1024) throw new Error('message exceeds 32 MiB');
    connection.send(text);
  };
  const snapshot = async () => {
    const identities = await browser.contextualIdentities.query({});
    const stores = ['firefox-default', ...identities.map(i => i.cookieStoreId)];
    const cookies = [];
    for (const storeId of stores) cookies.push(...await browser.cookies.getAll({storeId, partitionKey: {}, firstPartyDomain: null}));
    return {identities, cookies};
  };
  browser.cookies.onChanged.addListener(change => send({method: 'cookies.changed', params: {domain: change.cookie.domain, storeId: change.cookie.storeId, removed: change.removed}}));
  browser.webNavigation.onCompleted.addListener(async details => {
    if (details.frameId !== 0) return;
    try {
      const tab = await browser.tabs.get(details.tabId), u = new URL(details.url);
      if (u.protocol === 'http:' || u.protocol === 'https:') send({method: 'origin.observed', params: {origin: u.origin, storeId: tab.cookieStoreId}});
    } catch (_) { /* A tab may close between notification and lookup. */ }
  });
  function connect() {
    connection = new WebSocket(`ws://127.0.0.1:${cfg.wsPort}/companion`);
    connection.onopen = () => {
      delay = 1000;
      send({hello: true, protocolVersion: cfg.protocolVersion, extensionVersion: browser.runtime.getManifest().version, token: cfg.token, profileHint: cfg.profileHint});
    };
    let pending = 0;
    connection.onmessage = async event => {
      let request;
      try {
        if (typeof event.data !== 'string' || event.data.length > 32 * 1024 * 1024) throw new Error('invalid message');
        request = JSON.parse(event.data);
        if (!Number.isSafeInteger(request.id) || request.id <= 0) throw new Error('invalid request id');
        if (++pending > 16) throw new Error('too many requests');
        let result;
        if (request.method === 'snapshot') result = await snapshot();
        else if (request.method === 'readStorage' && globalThis.AgentCookieReadStorage) result = await globalThis.AgentCookieReadStorage(request.params);
        else throw new Error('unsupported method');
        send({id: request.id, result});
      } catch (error) {
        if (request && request.id) send({id: request.id, error: String(error.message || error)});
        else connection.close();
      } finally { pending = Math.max(0, pending - 1); }
    };
    connection.onclose = () => { setTimeout(connect, delay); delay = Math.min(60000, delay * 2); };
    connection.onerror = () => connection.close();
  }
  connect();
})();
