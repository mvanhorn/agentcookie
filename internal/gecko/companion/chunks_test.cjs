const { test } = require("node:test");
const assert = require("node:assert/strict");
const { createHash } = require("node:crypto");
require("../../storagecodec/codec.js");
require("./chunks.js");
globalThis.WebSocket = { OPEN: 1 };
test("negotiated sender keeps each frame below32MiB for a33MiB response", async () => {
  const result = "x".repeat(33 * 1024 * 1024);
  let count = 0,
    total = 0,
    digest;
  const sum = createHash("sha256");
  const socket = {
    readyState: 1,
    bufferedAmount: 0,
    send(text) {
      assert.ok(Buffer.byteLength(text) <= 32 * 1024 * 1024);
      const m = JSON.parse(text);
      assert.equal(m.id, 7);
      assert.equal(m.chunk.index, count++);
      assert.equal(m.chunk.count, 3);
      assert.equal(m.chunk.total, result.length + 2);
      if (digest) assert.equal(m.chunk.sha256, digest);
      digest = m.chunk.sha256;
      const part = Buffer.from(m.chunk.data, "base64");
      total += part.length;
      sum.update(part);
    },
  };
  await AgentCookieChunkedSend(socket, 7, result, true);
  assert.equal(count, 3);
  assert.equal(total, result.length + 2);
  assert.equal(sum.digest("hex"), digest);
});
test("sender refuses unnegotiated large reply, closed peer and aggregate over-cap", async () => {
  let writes = 0;
  const socket = {
    readyState: 1,
    bufferedAmount: 0,
    send() {
      writes++;
    },
  };
  await assert.rejects(
    AgentCookieChunkedSend(socket, 1, "x".repeat(33 * 1024 * 1024), false),
    /negotiated/,
  );
  assert.equal(writes, 0);
  socket.readyState = 0;
  await assert.rejects(
    AgentCookieChunkedSend(socket, 1, { ok: true }, true),
    /closed/,
  );
  const saved = globalThis.AgentCookieCodec;
  globalThis.AgentCookieCodec = { jsonBytes: () => 272 * 1024 * 1024 + 1 };
  try {
    await assert.rejects(
      AgentCookieChunkedSend(socket, 1, {}, true),
      /peer response/,
    );
  } finally {
    globalThis.AgentCookieCodec = saved;
  }
  socket.readyState = 1;
  await AgentCookieChunkedSend(socket, 2, { ok: true }, false);
  assert.equal(writes, 1);
});
test("small concurrent responses keep reservations until bounded socket queue drains", async () => {
  let peak = 0,
    completed = 0,
    writes = 0;
  const socket = {
    readyState: 1,
    bufferedAmount: 0,
    send(text) {
      this.bufferedAmount += Buffer.byteLength(text);
      peak = Math.max(peak, this.bufferedAmount);
      writes++;
    },
    close() {
      this.readyState = 3;
      this.bufferedAmount = 0;
    },
  };
  const calls = Array.from({ length: 6 }, (_, i) =>
    AgentCookieChunkedSend(
      socket,
      i + 1,
      "x".repeat(7 * 1024 * 1024),
      true,
    ).then(() => completed++),
  );
  await new Promise((r) => setTimeout(r, 20));
  assert.equal(completed, 0);
  assert.ok(peak <= 32 * 1024 * 1024);
  const drain = setInterval(() => {
    socket.bufferedAmount = 0;
  }, 10);
  try {
    await Promise.all(calls);
  } finally {
    clearInterval(drain);
  }
  assert.equal(writes, 6);
  assert.equal(completed, 6);
  assert.ok(peak <= 32 * 1024 * 1024);
});
