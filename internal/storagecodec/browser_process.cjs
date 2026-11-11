// Fixture-only process ownership. Never inspect or signal an unrelated process.
const { spawn } = require("node:child_process");
const path = require("node:path");
const owned = new WeakMap();
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
function options(dir, env = process.env) {
  return {
    args: [
      "--headless=new",
      "--disable-gpu",
      "--disable-background-networking",
      "--disable-breakpad",
      "--disable-crash-reporter",
      "--no-first-run",
      "--no-default-browser-check",
      "--use-mock-keychain",
      "--password-store=basic",
      "--remote-debugging-address=127.0.0.1",
      "--remote-debugging-port=0",
      "--user-data-dir=" + path.join(dir, "profile"),
      "about:blank",
    ],
    spawn: {
      detached: true,
      env: {
        ...env,
        HOME: dir,
        XDG_CONFIG_HOME: path.join(dir, "config"),
        TMPDIR: path.join(dir, "tmp"),
      },
      stdio: ["ignore", "ignore", "pipe"],
    },
  };
}
function launch(
  binary,
  dir,
  { spawnImpl = spawn, env = process.env, platform = process.platform } = {},
) {
  if (platform !== "linux" && platform !== "darwin")
    throw Error("fixture requires owned Unix process groups");
  const config = options(dir, env);
  if (process.getuid?.() === 0) config.args.unshift("--no-sandbox");
  const proc = spawnImpl(binary, config.args, config.spawn);
  const state = { closed: false, exited: false, done: null };
  proc.once("exit", () => {
    state.exited = true;
  });
  state.done = new Promise((resolve) => {
    proc.once("close", () => {
      state.closed = true;
      resolve();
    });
    proc.once("error", () => {
      if (!proc.pid) {
        state.closed = true;
        resolve();
      }
    });
  });
  owned.set(proc, state);
  return proc;
}
async function stop(proc, { signal = process.kill, timeoutMS = 3000 } = {}) {
  const state = owned.get(proc);
  if (!state) throw Error("refusing unowned fixture process");
  if (!Number.isSafeInteger(proc.pid) || proc.pid <= 1) {
    await state.done;
    return;
  }
  const exists = () => {
    try {
      signal(-proc.pid, 0);
      return true;
    } catch (error) {
      if (error.code === "ESRCH") return false;
      throw error;
    }
  };
  const reaped = () =>
    state.exited ||
    state.closed ||
    proc.exitCode != null ||
    proc.signalCode != null;
  const send = (name) => {
    if (reaped())
      throw Error(
        "fixture leader exited; refusing stale process-group signal and retaining profile",
      );
    try {
      signal(-proc.pid, name);
    } catch (error) {
      if (error.code !== "ESRCH") throw error;
    }
  };
  // Once Node reports leader exit, its PID/PGID may have been reused. Never
  // infer continuing ownership from group existence or send another signal.
  if (!reaped()) {
    send("SIGTERM");
    await Promise.race([state.done, sleep(timeoutMS)]);
    if (exists() && !reaped()) send("SIGKILL");
  }
  const deadline = Date.now() + timeoutMS;
  while (!state.closed || exists()) {
    if (Date.now() > deadline)
      throw Error(
        "owned fixture process group did not stop; retaining temporary profile",
      );
    await sleep(10);
  }
  owned.delete(proc);
}
module.exports = { options, launch, stop };
