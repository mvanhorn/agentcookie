// Loaded only in the privileged extension background, never the page world.
(() => {
  "use strict";
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
  globalThis.AgentCookieReadStorage = async (request) => {
    const origin = new URL(request.origin);
    if (
      origin.origin !== request.origin ||
      !["http:", "https:"].includes(origin.protocol)
    )
      throw Error("invalid storage origin");
    if (!/^[a-zA-Z0-9_-]{16,128}$/.test(request.nonce))
      throw Error("invalid storage nonce");
    if (!["intercept", "request"].includes(request.mode))
      throw Error("invalid read mode");
    if (!/^firefox-(default|container-[0-9]+)$/.test(request.cookieStoreId))
      throw Error("invalid cookie store");
    let tab,
      timedOut = false,
      timer;
    const deadline = Date.now() + 3000;
    const work = async () => {
      tab = await browser.tabs.create({
        url: request.origin + "/.agentcookie-read/" + request.nonce,
        active: false,
        cookieStoreId: request.cookieStoreId,
      });
      if (timedOut) {
        await browser.tabs.remove(tab.id);
        throw Error("origin timeout");
      }
      for (;;) {
        if (Date.now() > deadline) throw Error("origin timeout");
        let probe;
        try {
          const result = await browser.scripting.executeScript({
            target: { tabId: tab.id },
            world: "MAIN",
            func: () => ({
              origin: location.origin,
              title: document.title,
              ready: document.readyState,
            }),
          });
          probe = result[0] && result[0].result;
        } catch {}
        if (
          probe &&
          probe.origin === request.origin &&
          (request.mode === "request"
            ? probe.ready !== "loading"
            : probe.title === "agentcookie " + request.nonce)
        )
          break;
        await sleep(250);
      }
      await browser.tabs.hide(tab.id);
      await browser.scripting.executeScript({
        target: { tabId: tab.id },
        world: "MAIN",
        files: ["codec.js", "reader.js"],
      });
      const result = await browser.scripting.executeScript({
        target: { tabId: tab.id },
        world: "MAIN",
        func: (options) => globalThis.AgentCookieStorageReader.read(options),
        args: [
          {
            origin: request.origin,
            nonce: request.nonce,
            mode: request.mode,
            container: request.container || 0,
            localStorage: request.localStorage,
            indexedDB: request.indexedDB,
            budgetMS: Math.max(1, deadline - Date.now()),
          },
        ],
      });
      if (!result[0] || !result[0].result)
        throw Error("missing storage result");
      return result[0].result;
    };
    try {
      return await Promise.race([
        work(),
        new Promise((_, reject) => {
          timer = setTimeout(() => {
            timedOut = true;
            reject(Error("origin timeout"));
          }, 3000);
        }),
      ]);
    } finally {
      clearTimeout(timer);
      if (tab) await browser.tabs.remove(tab.id);
    }
  };
})();
