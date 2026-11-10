// Browser primitives only: the Go caller journals intent BEFORE calling these and
// completes it ONLY after comparing the returned canonical values with desired.
(() => {
  "use strict";
  const C = globalThis.AgentCookieCodec;
  const eq = (a, b) => JSON.stringify(a) === JSON.stringify(b);
  const keyPath = (v) => (v === null ? null : C.decode(v));
  const name = (v) => {
    const s = C.decode(v);
    if (typeof s !== "string") throw Error("invalid schema name");
    return s;
  };
  function assertOrigin(o) {
    if (
      location.origin !== o.origin ||
      document.title !== "agentcookie " + o.nonce
    )
      throw Error("helper origin or nonce mismatch");
  }
  function ls(o) {
    assertOrigin(o);
    const key = C.decode(o.key);
    if (typeof key !== "string") throw Error("invalid localStorage key");
    if (o.op === "delete") {
      localStorage.removeItem(key);
      return { absent: localStorage.getItem(key) === null };
    }
    if (o.op !== "set") throw Error("invalid operation");
    const value = C.decode(o.value);
    if (
      typeof value !== "string" ||
      2 * (key.length + value.length) > 10 * 1024 * 1024
    )
      throw Error("localStorage value bytes limit");
    localStorage.setItem(key, value);
    const observed = localStorage.getItem(key);
    return { absent: observed === null, value: C.encode(observed) };
  }
  function open(name, version, upgrade) {
    return new Promise((resolve, reject) => {
      const r = indexedDB.open(name, version);
      let failed = false;
      r.onblocked = () => {
        failed = true;
        reject(Error("blocked"));
      };
      r.onerror = () => reject(r.error);
      r.onupgradeneeded = () => {
        if (failed) {
          r.transaction.abort();
          return;
        }
        try {
          upgrade(r.result, r.transaction);
        } catch (e) {
          r.transaction.abort();
          reject(e);
        }
      };
      r.onsuccess = () => {
        if (failed) r.result.close();
        else resolve(r.result);
      };
    });
  }
  function inspect(db, ir) {
    let missing = false;
    for (const s of ir.stores) {
      if (!db.objectStoreNames.contains(name(s.name))) {
        missing = true;
        continue;
      }
      const tx = db.transaction(name(s.name)),
        actual = tx.objectStore(name(s.name));
      if (
        !eq(actual.keyPath, keyPath(s.keyPath)) ||
        actual.autoIncrement !== s.autoIncrement
      )
        throw Error("schema conflict: " + s.name);
      for (const i of s.indexes || []) {
        if (!actual.indexNames.contains(name(i.name))) {
          missing = true;
          continue;
        }
        const a = actual.index(name(i.name));
        if (
          !eq(a.keyPath, keyPath(i.keyPath)) ||
          a.unique !== i.unique ||
          a.multiEntry !== i.multiEntry
        )
          throw Error("schema conflict: " + s.name + "/" + i.name);
      }
    }
    return missing;
  }
  function upgrade(db, tx, ir) {
    for (const s of ir.stores) {
      const store = db.objectStoreNames.contains(name(s.name))
        ? tx.objectStore(name(s.name))
        : db.createObjectStore(name(s.name), {
            keyPath: keyPath(s.keyPath),
            autoIncrement: s.autoIncrement,
          });
      for (const i of s.indexes || [])
        if (!store.indexNames.contains(name(i.name)))
          store.createIndex(name(i.name), keyPath(i.keyPath), {
            unique: i.unique,
            multiEntry: i.multiEntry,
          });
    }
  }
  async function prepare(ir) {
    const inventory = await indexedDB.databases(),
      found = inventory.find((i) => i.name === name(ir.name));
    if (!found)
      return {
        db: await open(name(ir.name), ir.version, (db, tx) =>
          upgrade(db, tx, ir),
        ),
        schema: "created",
      };
    let db = await open(name(ir.name), found.version, () => {
      throw Error("database disappeared");
    });
    let missing;
    try {
      missing = inspect(db, ir);
    } catch (e) {
      db.close();
      throw e;
    }
    if (!missing) return { db, schema: "matched" };
    db.close();
    db = await open(name(ir.name), found.version + 1, (db, tx) =>
      upgrade(db, tx, ir),
    );
    return { db, schema: "upgraded to " + db.version };
  }
  function nextGenerator(db, storeName) {
    return new Promise((resolve, reject) => {
      const tx = db.transaction(storeName, "readwrite");
      let next, problem;
      const r = tx.objectStore(storeName).add({});
      r.onsuccess = () => {
        next = r.result;
        tx.abort();
      };
      r.onerror = () => {
        problem = r.error;
      };
      tx.onabort = () => (problem ? reject(problem) : resolve(next));
      tx.oncomplete = () =>
        reject(Error("generator probe unexpectedly committed"));
      tx.onerror = () => {};
    });
  }
  async function idb(o) {
    assertOrigin(o);
    const ir = o.database;
    // Decode every record before any schema/data mutation. Reject malformed keys.
    const writes = ir.stores.map((s) => ({
      schema: s,
      records: (s.records || []).map((r) => {
        const key = C.decode(r.key);
        C.encodeKey(key);
        return { key, value: C.decode(r.value) };
      }),
    }));
    const deletions = (o.deletes || []).map((d) => ({
      store: name(d.store),
      key: C.decode(d.key),
    }));
    for (const d of deletions) C.encodeKey(d.key);
    let db, schema;
    if (o.deletes_only) {
      const found = (await indexedDB.databases()).find(
        (d) => d.name === name(ir.name),
      );
      if (!found) return { schema: "absent", records: [] };
      db = await open(found.name, found.version, () => {
        throw Error("database disappeared");
      });
      schema = "matched";
      for (let i = deletions.length - 1; i >= 0; i--)
        if (!db.objectStoreNames.contains(deletions[i].store))
          deletions.splice(i, 1);
    } else {
      ({ db, schema } = await prepare(ir));
    }
    try {
      const names = [
        ...new Set([
          ...ir.stores.map((s) => name(s.name)),
          ...deletions.map((d) => d.store),
        ]),
      ];
      if (!names.length) return { schema, records: [] };
      await new Promise((resolve, reject) => {
        const tx = db.transaction(names, "readwrite");
        tx.oncomplete = resolve;
        tx.onabort = () => reject(tx.error || Error("transaction aborted"));
        tx.onerror = () => {};
        try {
          // Free only keys owned by this batch before issuing puts. All requests
          // share one transaction, so unique-index swaps work atomically and an
          // unowned conflicting record still aborts the entire batch.
          for (const d of deletions) tx.objectStore(d.store).delete(d.key);
          for (const s of writes)
            for (const r of s.records)
              tx.objectStore(name(s.schema.name)).delete(r.key);
          for (const s of writes) {
            const store = tx.objectStore(name(s.schema.name));
            for (const r of s.records)
              if (store.keyPath === null) store.put(r.value, r.key);
              else {
                const req = store.put(r.value);
                req.onsuccess = () => {
                  if (indexedDB.cmp(req.result, r.key) !== 0) tx.abort();
                };
              }
          }
        } catch (e) {
          tx.abort();
          reject(e);
        }
      });
      const records = [];
      await new Promise((resolve, reject) => {
        const tx = db.transaction(names, "readonly");
        tx.oncomplete = resolve;
        tx.onabort = () => reject(tx.error || Error("read-back aborted"));
        tx.onerror = () => {};
        const read = (store, key, op) => {
          const r = tx.objectStore(store).openCursor(IDBKeyRange.only(key));
          r.onsuccess = () => {
            try {
              records.push({
                store: C.encode(store),
                key: C.encodeKey(key),
                op,
                absent: !r.result,
                ...(r.result ? { value: C.encode(r.result.value) } : {}),
              });
            } catch (e) {
              tx.abort();
              reject(e);
            }
          };
        };
        for (const s of writes)
          for (const r of s.records) read(name(s.schema.name), r.key, "set");
        for (const d of deletions) read(d.store, d.key, "delete");
      });
      // Includes actual schema for the caller's verification; no database deletion.
      const actual = {
        name: C.encode(db.name),
        version: db.version,
        stores: ir.stores.map((s) => {
          const t = db.transaction(name(s.name)),
            a = t.objectStore(name(s.name));
          return {
            name: s.name,
            keyPath: a.keyPath === null ? null : C.encode(a.keyPath),
            autoIncrement: a.autoIncrement,
            indexes: Array.from(a.indexNames, (n) => {
              const i = a.index(n);
              return {
                name: C.encode(n),
                keyPath: C.encode(i.keyPath),
                unique: i.unique,
                multiEntry: i.multiEntry,
              };
            }),
          };
        }),
      };
      let generatorGap = false;
      for (const s of ir.stores)
        if (s.autoIncrement && s.generator !== undefined) {
          try {
            if ((await nextGenerator(db, name(s.name))) < s.generator)
              generatorGap = true;
          } catch {
            generatorGap = true;
          }
        }
      return {
        schema,
        actual_schema: actual,
        records,
        generator_gap: generatorGap,
      };
    } finally {
      db.close();
    }
  }
  function readLS(o) {
    assertOrigin(o);
    const key = C.decode(o.key);
    if (typeof key !== "string") throw Error("invalid localStorage key");
    const value = localStorage.getItem(key);
    return value === null
      ? { absent: true }
      : { absent: false, value: C.encode(value) };
  }
  async function readIDB(o) {
    assertOrigin(o);
    const infos = await indexedDB.databases(),
      found = infos.find((d) => d.name === name(o.database.name));
    if (!found) return { absent: true };
    const db = await open(found.name, found.version, () => {
      throw Error("database disappeared");
    });
    try {
      if (!db.objectStoreNames.contains(name(o.store))) return { absent: true };
      if (!o.deleted && inspect(db, o.database)) throw Error("schema missing");
      return await new Promise((resolve, reject) => {
        const tx = db.transaction(name(o.store), "readonly");
        let observed;
        const r = tx
          .objectStore(name(o.store))
          .openCursor(IDBKeyRange.only(C.decode(o.key)));
        r.onsuccess = () => {
          try {
            observed = r.result
              ? { absent: false, value: C.encode(r.result.value) }
              : { absent: true };
          } catch (e) {
            tx.abort();
            reject(e);
          }
        };
        tx.oncomplete = () => resolve(observed);
        tx.onabort = () => reject(tx.error || Error("read-back aborted"));
        tx.onerror = () => {};
      });
    } finally {
      db.close();
    }
  }
  async function readSchema(o) {
    assertOrigin(o);
    const infos = await indexedDB.databases(),
      found = infos.find((d) => d.name === name(o.database.name));
    if (!found) return { absent: true };
    const db = await open(found.name, found.version, () => {
      throw Error("database disappeared");
    });
    try {
      if (inspect(db, o.database)) return { absent: true };
      return { absent: false, value: o.database };
    } finally {
      db.close();
    }
  }
  async function readBatch(o) {
    assertOrigin(o);
    const stores = (o.stores || []).map((s) => ({
      name: name(s.name),
      keys: s.keys.map((k) => {
        const key = C.decode(k);
        C.encodeKey(key);
        return key;
      }),
    }));
    const out = {
      schema: false,
      stores: stores.map((s) => s.keys.map(() => ({ absent: true }))),
    };
    C.jsonBytes(out, 128 * 1024 * 1024);
    const found = (await indexedDB.databases()).find(
      (d) => d.name === name(o.database.name),
    );
    if (!found) return out;
    const db = await open(found.name, found.version, () => {
      throw Error("database disappeared");
    });
    try {
      // Schema compatibility is separate from record visibility. A drifted
      // index must not prevent hash-verified revocation of owned record keys.
      if (Array.isArray(o.database.stores)) {
        try {
          out.schema = !inspect(db, o.database);
        } catch {
          out.schema = false;
        }
      }
      const names = stores
        .filter((s) => db.objectStoreNames.contains(s.name))
        .map((s) => s.name);
      if (!names.length) return out;
      // Reserve a compact per-record error for keys not yet processed. Release
      // that reservation as each cursor returns, then charge the replacement
      // delta. Even a database full of unsupported local values stays bounded.
      let bytes =
        C.jsonBytes(out, 128 * 1024 * 1024) +
        stores.reduce((n, s) => n + s.keys.length * 64, 0);
      if (bytes > 128 * 1024 * 1024) throw Error("readback bytes limit");
      await new Promise((resolve, reject) => {
        const tx = db.transaction(names, "readonly");
        tx.oncomplete = resolve;
        tx.onabort = () => reject(tx.error || Error("read-back aborted"));
        tx.onerror = () => {};
        for (let n = 0; n < stores.length; n++) {
          const s = stores[n];
          if (!db.objectStoreNames.contains(s.name)) continue;
          const store = tx.objectStore(s.name);
          for (let k = 0; k < s.keys.length; k++) {
            const request = store.openCursor(IDBKeyRange.only(s.keys[k]));
            request.onsuccess = () => {
              bytes -= 64;
              const previousSize = C.jsonBytes(out.stores[n][k]);
              try {
                if (request.result) {
                  const value = {
                    absent: false,
                    value: C.encode(request.result.value),
                  };
                  const size = C.jsonBytes(value, 16 * 1024 * 1024);
                  if (bytes + size - previousSize > 128 * 1024 * 1024)
                    throw Error("readback bytes limit");
                  bytes += size - previousSize;
                  out.stores[n][k] = value;
                }
              } catch (error) {
                // A destination-local unsupported value cannot poison unrelated
                // owned keys or make an already-relinquished tombstone a blocker.
                const result = {
                  absent: false,
                  error: String(error).includes("bytes limit")
                    ? "readback_bytes_limit"
                    : "unsupported_value",
                };
                bytes += C.jsonBytes(result) - previousSize;
                out.stores[n][k] = result;
              }
            };
          }
        }
      });
      return out;
    } finally {
      db.close();
    }
  }
  globalThis.AgentCookieStorageApplier = Object.freeze({
    localStorage: ls,
    indexedDB: idb,
    readLocalStorage: readLS,
    readIndexedDB: readIDB,
    readIndexedDBBatch: readBatch,
    readSchema,
  });
})();
