// Privileged background transport only. Tokens never enter page-world code.
(() => {
  "use strict";
  const CHUNK_BYTES = 16 * 1024 * 1024;
  const MAX_FRAME = 32 * 1024 * 1024;
  const MAX_IN_FLIGHT = 272 * 1024 * 1024;
  let reserved = 0;
  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
  async function boundedSend(socket, text, deadline) {
    const bytes = new TextEncoder().encode(text).length;
    if (bytes > MAX_FRAME) throw Error("frame bytes limit");
    while (socket.bufferedAmount > MAX_FRAME - bytes) {
      if (socket.readyState !== WebSocket.OPEN || Date.now() > deadline)
        throw Error("send timeout or disconnect");
      await sleep(10);
    }
    if (socket.readyState !== WebSocket.OPEN || Date.now() > deadline)
      throw Error("connection closed or send timeout");
    socket.send(text);
    // Retain this response's reservation until the network queue has drained.
    while (socket.bufferedAmount > 0) {
      if (socket.readyState !== WebSocket.OPEN || Date.now() > deadline) {
        socket.close();
        throw Error("send timeout or disconnect");
      }
      await sleep(10);
    }
  }
  globalThis.AgentCookieChunkedSend = async (socket, id, result, enabled) => {
    // The codec counts UTF8 wire bytes before the JSON/string/byte allocations.
    const count = globalThis.AgentCookieCodec
      ? globalThis.AgentCookieCodec.jsonBytes(result, MAX_IN_FLIGHT)
      : new TextEncoder().encode(JSON.stringify(result)).length;
    if (count > MAX_IN_FLIGHT - reserved)
      throw Error("peer response bytes limit");
    reserved += count;
    try {
      const deadline = Date.now() + 30000;
      const text = JSON.stringify(result);
      if (count + 128 <= MAX_FRAME) {
        if (socket.readyState !== WebSocket.OPEN)
          throw Error("connection closed");
        await boundedSend(socket, `{"id":${id},"result":${text}}`, deadline);
        return;
      }
      if (!enabled) throw Error("response requires negotiated chunk support");
      const bytes = new TextEncoder().encode(text);
      const digest = await crypto.subtle.digest("SHA-256", bytes);
      const sha256 = Array.from(new Uint8Array(digest), (b) =>
        b.toString(16).padStart(2, "0"),
      ).join("");
      const chunks = Math.ceil(bytes.length / CHUNK_BYTES);
      for (let index = 0; index < chunks; index++) {
        const part = bytes.subarray(
          index * CHUNK_BYTES,
          (index + 1) * CHUNK_BYTES,
        );
        let binary = "";
        for (let offset = 0; offset < part.length; offset += 8192)
          binary += String.fromCharCode(
            ...part.subarray(offset, offset + 8192),
          );
        await boundedSend(
          socket,
          JSON.stringify({
            id,
            chunk: {
              index,
              count: chunks,
              total: bytes.length,
              sha256,
              data: btoa(binary),
            },
          }),
          deadline,
        );
      }
    } finally {
      reserved -= count;
    }
  };
})();
