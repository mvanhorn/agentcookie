// Explicit opt-in: all browser state and HOME are throwaway fixture directories.
const { test } = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const os = require("node:os");
const path = require("node:path");
const http = require("node:http");
const fixtureProcess = require("./browser_process.cjs");
test(
  "Chromium MAIN-world codec, fresh IDB v1, schema, atomicity, read-back, ownership and generator",
  { skip: process.env.AGENTCOOKIE_BROWSER_TEST !== "1", timeout: 60000 },
  async (t) => {
    const dir = await fs.mkdtemp(
      path.join(os.tmpdir(), "agentcookie-storage-fixture-"),
    );
    let proc, ws;
    const server = http.createServer((req, res) => {
      res.setHeader("Content-Type", "text/html");
      res.end("<!doctype html><title>agentcookie fixture_nonce_1234</title>");
    });
    try {
      await new Promise((r) => server.listen(0, "127.0.0.1", r));
      const origin = "http://127.0.0.1:" + server.address().port;
      await fs.mkdir(path.join(dir, "config"), { recursive: true });
      await fs.mkdir(path.join(dir, "tmp"), { recursive: true });
      proc = fixtureProcess.launch(
        process.env.AGENTCOOKIE_CHROME_BINARY ||
          process.env.CHROME_BIN ||
          "/usr/bin/chromium",
        dir,
      );
      let stderr = "";
      proc.stderr.on("data", (d) => {
        stderr = (stderr + d).slice(-65536);
      });
      let lines;
      for (let n = 0; n < 100; n++) {
        try {
          lines = (
            await fs.readFile(
              path.join(dir, "profile", "DevToolsActivePort"),
              "utf8",
            )
          ).split("\n");
          break;
        } catch {}
        await new Promise((r) => setTimeout(r, 100));
      }
      assert.ok(lines, "fixture Chromium starts: " + stderr);
      const tabs = await (
        await fetch("http://127.0.0.1:" + lines[0] + "/json/list")
      ).json();
      ws = new WebSocket(tabs[0].webSocketDebuggerUrl);
      await new Promise((resolve, reject) => {
        ws.onopen = resolve;
        ws.onerror = reject;
      });
      let seq = 0;
      const pending = new Map();
      ws.onmessage = ({ data }) => {
        const m = JSON.parse(data);
        if (m.id) {
          const p = pending.get(m.id);
          pending.delete(m.id);
          if (m.error) p.reject(Error(JSON.stringify(m.error)));
          else p.resolve(m.result);
        }
      };
      const call = (method, params = {}) =>
        new Promise((resolve, reject) => {
          const id = ++seq;
          pending.set(id, { resolve, reject });
          ws.send(JSON.stringify({ id, method, params }));
        });
      await call("Page.navigate", { url: origin });
      for (let n = 0; n < 50; n++) {
        const r = await call("Runtime.evaluate", {
          expression: "document.readyState",
        });
        if (r.result.value === "complete") break;
        await new Promise((r) => setTimeout(r, 50));
      }
      for (const f of ["codec.js", "reader.js", "applier.js"])
        await call("Runtime.evaluate", {
          expression: await fs.readFile(path.join(__dirname, f), "utf8"),
        });
      const result = await call("Runtime.evaluate", {
        awaitPromise: true,
        returnByValue: true,
        expression: `${await fs.readFile(path.join(__dirname, "browser_fixture.js"), "utf8")}
AgentCookieStorageFixture()`,
      });
      if (result.exceptionDetails)
        throw Error(JSON.stringify(result.exceptionDetails));
      assert.equal(result.result.value.checks, 17);
      console.log(result.result.value);
    } finally {
      if (ws) ws.close();
      let cleanupError;
      try {
        if (proc) await fixtureProcess.stop(proc);
      } catch (error) {
        cleanupError = error;
      }
      server.closeAllConnections();
      await new Promise((r) => server.close(r));
      if (cleanupError) throw cleanupError;
      await fs.rm(dir, { recursive: true, force: true });
    }
  },
);
