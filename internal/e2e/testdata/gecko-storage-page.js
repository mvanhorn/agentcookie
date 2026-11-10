(async () => {
  const query = new URL(location.href).searchParams;
  const container = Number(query.get("container") || 0);
  const request = (r) =>
    new Promise((resolve, reject) => {
      r.onsuccess = () => resolve(r.result);
      r.onerror = () => reject(r.error);
    });
  async function database() {
    const r = indexedDB.open("fixture-auth", 1);
    r.onupgradeneeded = () => {
      r.result.createObjectStore("tokens");
      const s = r.result.createObjectStore("sequence", { autoIncrement: true });
      s.createIndex("label", "label", { unique: true });
    };
    return request(r);
  }
  async function transact(db, body) {
    await new Promise((resolve, reject) => {
      const tx = db.transaction([...db.objectStoreNames], "readwrite");
      tx.oncomplete = resolve;
      tx.onabort = () => reject(tx.error);
      body(tx);
    });
  }
  async function controlled() {
    await navigator.serviceWorker.register("/sw.js");
    await navigator.serviceWorker.ready;
    if (!navigator.serviceWorker.controller)
      await new Promise((r) =>
        navigator.serviceWorker.addEventListener("controllerchange", r, {
          once: true,
        }),
      );
  }
  try {
    if (location.pathname === "/seed") {
      localStorage.setItem("auth", "token-" + container);
      localStorage.setItem("key\ud800", "value\udfff");
      const buffer = new ArrayBuffer(64),
        rich = {
          nil: null,
          undef: undefined,
          values: [NaN, Infinity, -Infinity, -0],
          big: 123456789012345678901n,
          text: "x\ud800😀",
          date: new Date(1234),
          invalid: new Date(NaN),
          regexp: /fixture/giu,
          buffer,
          view: new Uint16Array(buffer, 4, 4),
          dataView: new DataView(buffer, 2, 8),
          boxed: [
            new Number(-0),
            new String("\udfff"),
            new Boolean(true),
            Object(44n),
          ],
        };
      rich.self = rich;
      rich.alias = rich.date;
      rich.sparse = new Array(4);
      rich.sparse[2] = undefined;
      rich.sparse[3] = rich;
      rich.map = new Map([[rich, rich.sparse]]);
      rich.set = new Set([rich, rich.map]);
      rich.typed = [];
      for (const kind of [
        "Int8Array",
        "Uint8Array",
        "Uint8ClampedArray",
        "Int16Array",
        "Uint16Array",
        "Int32Array",
        "Uint32Array",
        "Float16Array",
        "Float32Array",
        "Float64Array",
        "BigInt64Array",
        "BigUint64Array",
      ])
        if (typeof globalThis[kind] === "function")
          rich.typed.push(new globalThis[kind](buffer, 0, 2));
      Object.defineProperty(rich, "__proto__", {
        value: "safe",
        enumerable: true,
        writable: true,
        configurable: true,
      });
      const db = await database();
      await transact(db, (tx) => {
        tx.objectStore("tokens").put("token-" + container, "auth");
        tx.objectStore("tokens").put(rich, "rich");
        tx.objectStore("sequence").add({ label: "seed" });
      });
      db.close();
      await controlled();
      await fetch("/report", {
        method: "POST",
        body: JSON.stringify({ kind: "seed", container }),
      });
    } else if (location.pathname === "/logout") {
      localStorage.clear();
      const db = await database();
      await transact(db, (tx) => {
        for (const n of db.objectStoreNames) tx.objectStore(n).clear();
      });
      db.close();
      await fetch("/report", {
        method: "POST",
        body: JSON.stringify({ kind: "logout", container }),
      });
    } else {
      await controlled();
      globalThis.storageEvents = [];
      addEventListener("storage", (e) => {
        storageEvents.push(e.key);
        fetch("/storage-beacon").catch(() => {});
      });
      globalThis.fixtureReady = true;
      await fetch("/report", {
        method: "POST",
        body: JSON.stringify({ kind: "hostile", container }),
      });
    }
  } catch (error) {
    await fetch("/report", {
      method: "POST",
      body: JSON.stringify({ kind: "error", error: String(error), container }),
    });
  }
})();
