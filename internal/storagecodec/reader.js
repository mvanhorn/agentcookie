(() => {
  "use strict";
  const C = globalThis.AgentCookieCodec;
  const MAX_DB = 64 * 1024 * 1024,
    MAX_SURFACE = 128 * 1024 * 1024;
  const error = (e) => String((e && e.message) || e);
  function open(name, version) {
    return new Promise((resolve, reject) => {
      const r =
        version === undefined
          ? indexedDB.open(name)
          : indexedDB.open(name, version);
      let failed = false;
      r.onerror = () => reject(r.error);
      r.onblocked = () => {
        failed = true;
        reject(Error("blocked"));
      };
      r.onupgradeneeded = () => {
        r.transaction.abort();
        reject(Error("database disappeared during read"));
      };
      r.onsuccess = () => {
        if (failed) r.result.close();
        else resolve(r.result);
      };
    });
  }
  function generator(db, store) {
    return new Promise((resolve, reject) => {
      const tx = db.transaction(C.decode(store.name), "readwrite"),
        s = tx.objectStore(C.decode(store.name));
      let next, problem;
      const r = s.add({});
      r.onsuccess = () => {
        next = r.result;
        tx.abort();
      };
      r.onerror = () => {
        problem = r.error;
      };
      tx.onabort = () => (problem ? reject(problem) : resolve(next));
      tx.onerror = () => {};
      tx.oncomplete = () =>
        reject(Error("generator probe unexpectedly committed"));
    });
  }
  async function database(info, deadline) {
    const db = await open(info.name, info.version);
    try {
      const out = {
        name: C.encode(db.name),
        version: db.version,
        stores: [],
        status: { state: "complete" },
      };
      if (!db.objectStoreNames.length) return out;
      let bytes = C.jsonBytes(out, MAX_DB);
      await new Promise((resolve, reject) => {
        const tx = db.transaction(Array.from(db.objectStoreNames), "readonly");
        let failure;
        const abort = (e) => {
          failure = e;
          try {
            tx.abort();
          } catch {}
        };
        tx.oncomplete = resolve;
        tx.onabort = () => reject(failure || tx.error || Error("read aborted"));
        tx.onerror = () => {};
        for (const name of db.objectStoreNames) {
          const s = tx.objectStore(name),
            store = {
              name: C.encode(name),
              keyPath: s.keyPath === null ? null : C.encode(s.keyPath),
              autoIncrement: s.autoIncrement,
              indexes: [],
              records: [],
            };
          out.stores.push(store);
          for (const iname of s.indexNames) {
            const i = s.index(iname);
            store.indexes.push({
              name: C.encode(iname),
              keyPath: C.encode(i.keyPath),
              unique: i.unique,
              multiEntry: i.multiEntry,
            });
          }
          bytes += C.jsonBytes(store, MAX_DB) + 1;
          if (bytes > MAX_DB) {
            abort(Error("database bytes limit"));
            return;
          }
          // Cursor events yield to the browser without making the transaction
          // inactive. Flush bounded groups; never materialize getAll().
          let batch = [];
          const flush = () => {
            store.records.push(...batch);
            batch = [];
          };
          const r = s.openCursor();
          r.onerror = () => abort(r.error);
          r.onsuccess = () => {
            const cursor = r.result;
            if (!cursor) {
              flush();
              return;
            }
            try {
              if (Date.now() > deadline) throw Error("origin timeout");
              const record = {
                key: C.encodeKey(cursor.primaryKey),
                value: C.encode(cursor.value),
              };
              bytes += C.jsonBytes(record, 16 * 1024 * 1024) + 1;
              if (bytes > MAX_DB) throw Error("database bytes limit");
              batch.push(record);
              if (batch.length === 500) flush();
              cursor.continue();
            } catch (e) {
              abort(e);
            }
          };
        }
      });
      for (const s of out.stores)
        if (s.autoIncrement) {
          if (Date.now() > deadline) throw Error("origin timeout");
          s.generator = await generator(db, s);
        }
      C.jsonBytes(out, MAX_DB);
      return out;
    } finally {
      db.close();
    }
  }
  async function read(options) {
    if (location.origin !== options.origin) throw Error("origin mismatch");
    if (
      options.mode !== "request" &&
      document.title !== "agentcookie " + options.nonce
    )
      throw Error("nonce mismatch");
    const deadline = Date.now() + Math.min(3000, options.budgetMS || 3000),
      out = {
        origin: options.origin,
        container: options.container || 0,
        local_storage: [],
        indexed_db: [],
        local_storage_status: { state: "complete" },
        indexed_db_status: { state: "complete" },
      };
    let lsBytes = 2,
      idbBytes = 2;
    if (options.localStorage === false)
      out.local_storage_status = {
        state: "unsupported",
        reason: "storage read disabled",
      };
    else
      try {
        for (let i = 0; i < localStorage.length; i++) {
          if (Date.now() > deadline) throw Error("origin timeout");
          const key = localStorage.key(i),
            value = localStorage.getItem(key);
          if (value === null) throw Error("localStorage changed during read");
          if (value.length * 2 > 10 * 1024 * 1024)
            throw Error("localStorage value bytes limit");
          const item = { key: C.encode(key), value: C.encode(value) };
          lsBytes += C.jsonBytes(item, 16 * 1024 * 1024) + 1;
          if (lsBytes > MAX_SURFACE) throw Error("surface bytes limit");
          out.local_storage.push(item);
        }
      } catch (e) {
        out.local_storage = [];
        out.local_storage_status = { state: "incomplete", reason: error(e) };
      }
    if (options.indexedDB === false)
      out.indexed_db_status = {
        state: "unsupported",
        reason: "storage read disabled",
      };
    else
      try {
        if (typeof indexedDB.databases !== "function")
          throw Error("indexedDB.databases unsupported");
        const infos = await indexedDB.databases();
        for (const info of infos) {
          if (Date.now() > deadline) throw Error("origin timeout");
          try {
            const data = await database(info, deadline);
            idbBytes += C.jsonBytes(data, MAX_DB) + 1;
            if (idbBytes > MAX_SURFACE) throw Error("surface bytes limit");
            out.indexed_db.push(data);
          } catch (e) {
            out.indexed_db.push({
              name: C.encode(info.name),
              version: info.version,
              stores: [],
              status: { state: "incomplete", reason: error(e) },
            });
            out.indexed_db_status = { state: "incomplete", reason: error(e) };
          }
        }
      } catch (e) {
        out.indexed_db_status = { state: "incomplete", reason: error(e) };
      }
    return out;
  }
  globalThis.AgentCookieStorageReader = Object.freeze({ read, database });
})();
