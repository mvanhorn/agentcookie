const { test } = require("node:test"),
  assert = require("node:assert/strict"),
  { EventEmitter } = require("node:events");
const fixture = require("./browser_process.cjs");
test("browser fixture isolates home/config/tmp, credentials and debugging", () => {
  const o = fixture.options("/fixture", {
    HOME: "/real",
    TMPDIR: "/real/tmp",
    XDG_CONFIG_HOME: "/real/config",
  });
  for (const flag of [
    "--use-mock-keychain",
    "--password-store=basic",
    "--remote-debugging-address=127.0.0.1",
    "--user-data-dir=/fixture/profile",
  ])
    assert.ok(o.args.includes(flag));
  assert.equal(o.spawn.detached, true);
  assert.equal(o.spawn.env.HOME, "/fixture");
  assert.equal(o.spawn.env.TMPDIR, "/fixture/tmp");
  assert.equal(o.spawn.env.XDG_CONFIG_HOME, "/fixture/config");
});
test("cleanup signals only its owned detached group and waits after SIGKILL", async () => {
  const p = new EventEmitter();
  p.pid = 123456;
  const child = fixture.launch("unused", "/fixture", {
    spawnImpl: () => p,
    platform: "darwin",
  });
  let alive = true;
  const signals = [];
  await fixture.stop(child, {
    timeoutMS: 20,
    signal: (pid, sig) => {
      assert.equal(pid, -123456);
      if (!alive) {
        const e = Error("gone");
        e.code = "ESRCH";
        throw e;
      }
      signals.push(sig);
      if (sig === "SIGKILL")
        setTimeout(() => {
          alive = false;
          p.emit("close");
        }, 5);
    },
  });
  assert.ok(signals.includes("SIGTERM"));
  assert.ok(signals.includes("SIGKILL"));
  assert.equal(alive, false);
  await assert.rejects(fixture.stop({ pid: process.pid }), /unowned/);
});
test("cleanup never signals a stale group after its leader was reaped", async () => {
  const p = new EventEmitter();
  p.pid = 234567;
  const child = fixture.launch("unused", "/fixture", {
    spawnImpl: () => p,
    platform: "linux",
  });
  p.emit("exit", 0);
  p.emit("close", 0);
  const signals = [];
  await assert.rejects(
    fixture.stop(child, {
      timeoutMS: 10,
      signal: (pid, sig) => {
        assert.equal(pid, -234567);
        signals.push(sig);
      },
    }),
    /retaining temporary profile/,
  );
  assert.ok(signals.length > 0);
  assert.ok(signals.every((sig) => sig === 0));
});
