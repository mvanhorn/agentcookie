const { test } = require("node:test");
const assert = require("node:assert/strict");
require("./read-storage.js");
const request = {
  origin: "https://example.test",
  nonce: "fixture_nonce_1234",
  mode: "intercept",
  cookieStoreId: "firefox-container-2",
  container: 2,
};
test("companion injects MAIN only after nonce proof, hides after ready and always removes", async () => {
  const calls = [];
  globalThis.browser = {
    tabs: {
      create: async (arg) => {
        calls.push(["create", arg]);
        return { id: 12 };
      },
      hide: async (id) => calls.push(["hide", id]),
      remove: async (id) => calls.push(["remove", id]),
    },
    scripting: {
      executeScript: async (arg) => {
        calls.push(["execute", arg]);
        if (arg.files) return [];
        if (arg.args)
          return [{ result: { origin: request.origin, container: 2 } }];
        return [
          {
            result: {
              origin: request.origin,
              title: "agentcookie " + request.nonce,
              ready: "complete",
            },
          },
        ];
      },
    },
  };
  assert.deepEqual(await AgentCookieReadStorage(request), {
    origin: request.origin,
    container: 2,
  });
  assert.equal(calls[0][1].cookieStoreId, "firefox-container-2");
  assert.equal(calls[1][1].world, "MAIN");
  assert.equal(calls[2][0], "hide");
  assert.deepEqual(calls[3][1].files, ["codec.js", "reader.js"]);
  assert.deepEqual(calls.at(-1), ["remove", 12]);
  browser.scripting.executeScript = async (arg) => {
    if (arg.args) throw Error("codec rejected");
    if (arg.files) return [];
    return [
      {
        result: {
          origin: request.origin,
          title: "agentcookie " + request.nonce,
        },
      },
    ];
  };
  await assert.rejects(AgentCookieReadStorage(request), /codec rejected/);
  assert.deepEqual(calls.at(-1), ["remove", 12]);
});
test("request fallback accepts real document without fabricated nonce title", async () => {
  let removed = false;
  globalThis.browser = {
    tabs: {
      create: async () => ({ id: 1 }),
      hide: async () => {},
      remove: async () => {
        removed = true;
      },
    },
    scripting: {
      executeScript: async (arg) =>
        arg.files
          ? []
          : arg.args
            ? [{ result: { ok: true } }]
            : [
                {
                  result: {
                    origin: request.origin,
                    title: "404",
                    ready: "complete",
                  },
                },
              ],
    },
  };
  assert.deepEqual(
    await AgentCookieReadStorage({ ...request, mode: "request" }),
    { ok: true },
  );
  assert.ok(removed);
});
test("rejects malformed origin/mode/container/nonce before tab creation", async () => {
  globalThis.browser = {
    tabs: {
      create: () => {
        throw Error("should not create");
      },
    },
  };
  for (const change of [
    { origin: "https://evil.test/x" },
    { mode: "auto" },
    { nonce: "<script>" },
    { cookieStoreId: "firefox-private" },
  ])
    await assert.rejects(
      AgentCookieReadStorage({ ...request, ...change }),
      /invalid/,
    );
});
