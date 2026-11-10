// fake-indexeddb tests establish algorithm behavior, NOT browser compatibility.
const { test } = require("node:test");
const assert = require("node:assert/strict");
require("fake-indexeddb/auto");
require("./codec.js");
require("./reader.js");
require("./applier.js");
require("./browser_fixture.js");
globalThis.location = { origin: "http://127.0.0.1:1234" };
globalThis.document = { title: "agentcookie fixture_nonce_1234" };
const data = new Map();
globalThis.localStorage = {
  get length() {
    return data.size;
  },
  key: (i) => [...data.keys()][i],
  getItem: (k) => (data.has(k) ? data.get(k) : null),
  setItem: (k, v) => data.set(k, v),
  removeItem: (k) => data.delete(k),
};
test("IDB algorithm fixtures: schema, transactions, ownership, generator (not a browser test)", async () => {
  assert.equal((await AgentCookieStorageFixture()).checks, 17);
});
test("IDB names are lossless UTF16", async () => {
  const C = AgentCookieCodec;
  const database = {
    name: C.encode("db\ud800"),
    version: 1,
    stores: [
      {
        name: C.encode("store\udfff"),
        keyPath: null,
        autoIncrement: false,
        indexes: [
          {
            name: C.encode("index\ud800"),
            keyPath: C.encode("x"),
            unique: false,
            multiEntry: false,
          },
        ],
        records: [{ key: C.encodeKey("k"), value: C.encode({ x: "y" }) }],
      },
    ],
  };
  const out = await AgentCookieStorageApplier.indexedDB({
    origin: location.origin,
    nonce: "fixture_nonce_1234",
    database,
  });
  assert.deepEqual(out.actual_schema.name, database.name);
  assert.deepEqual(out.actual_schema.stores[0].name, database.stores[0].name);
  assert.deepEqual(
    out.actual_schema.stores[0].indexes[0].name,
    database.stores[0].indexes[0].name,
  );
  const read = await AgentCookieStorageReader.database(
    { name: "db\ud800", version: 1 },
    Date.now() + 3000,
  );
  assert.deepEqual(read.name, database.name);
  assert.deepEqual(read.stores[0].name, database.stores[0].name);
});
test("disabled surfaces never touch denied page storage APIs", async () => {
  const oldLS = globalThis.localStorage,
    oldIDB = globalThis.indexedDB;
  try {
    Object.defineProperty(globalThis, "localStorage", {
      configurable: true,
      get() {
        throw Error("LS access denied");
      },
    });
    Object.defineProperty(globalThis, "indexedDB", {
      configurable: true,
      get() {
        throw Error("IDB access denied");
      },
    });
    const out = await AgentCookieStorageReader.read({
      origin: location.origin,
      nonce: "fixture_nonce_1234",
      localStorage: false,
      indexedDB: false,
    });
    assert.equal(out.local_storage_status.state, "unsupported");
    assert.equal(out.indexed_db_status.state, "unsupported");
  } finally {
    Object.defineProperty(globalThis, "localStorage", {
      configurable: true,
      writable: true,
      value: oldLS,
    });
    Object.defineProperty(globalThis, "indexedDB", {
      configurable: true,
      writable: true,
      value: oldIDB,
    });
  }
});
test("unique-index swaps and replacements commit atomically without deleting unowned conflicts", async () => {
  const C = AgentCookieCodec,
    apply = AgentCookieStorageApplier.indexedDB;
  const db = {
    name: C.encode("unique-swap"),
    version: 1,
    stores: [
      {
        name: C.encode("s"),
        keyPath: null,
        autoIncrement: false,
        indexes: [
          {
            name: C.encode("u"),
            keyPath: C.encode("u"),
            unique: true,
            multiEntry: false,
          },
        ],
        records: [],
      },
    ],
  };
  const write = async (values, deletes = []) => {
    db.stores[0].records = values.map(([k, u]) => ({
      key: C.encodeKey(k),
      value: C.encode({ u }),
    }));
    return apply({
      origin: location.origin,
      nonce: "fixture_nonce_1234",
      database: db,
      deletes: deletes.map((k) => ({
        store: C.encode("s"),
        key: C.encodeKey(k),
      })),
    });
  };
  await write([
    [1, "A"],
    [2, "B"],
    [9, "unowned"],
  ]);
  await write([
    [1, "B"],
    [2, "A"],
  ]);
  await write([[3, "B"]], [1]);
  await assert.rejects(write([[2, "unowned"]]), /Constraint|transaction/);
  const out = await AgentCookieStorageReader.database(
    { name: "unique-swap", version: 1 },
    Date.now() + 3000,
  );
  const records = new Map(
    out.stores[0].records.map((r) => [C.decode(r.key), C.decode(r.value).u]),
  );
  assert.deepEqual(
    records,
    new Map([
      [2, "A"],
      [3, "B"],
      [9, "unowned"],
    ]),
  );
});
test("database readback batches existing, undefined and absent keys", async () => {
  const C = AgentCookieCodec,
    database = {
      name: C.encode("batch-read"),
      version: 1,
      stores: [
        {
          name: C.encode("s"),
          keyPath: null,
          autoIncrement: false,
          indexes: [],
          records: [
            { key: C.encodeKey("undefined"), value: C.encode(undefined) },
          ],
        },
      ],
    };
  await AgentCookieStorageApplier.indexedDB({
    origin: location.origin,
    nonce: "fixture_nonce_1234",
    database,
  });
  const out = await AgentCookieStorageApplier.readIndexedDBBatch({
    origin: location.origin,
    nonce: "fixture_nonce_1234",
    database,
    stores: [
      {
        name: C.encode("s"),
        keys: [C.encodeKey("undefined"), C.encodeKey("missing")],
      },
      { name: C.encode("absent-store"), keys: [C.encodeKey("k")] },
    ],
  });
  assert.equal(out.schema, true);
  assert.deepEqual(out.stores, [
    [{ absent: false, value: C.encode(undefined) }, { absent: true }],
    [{ absent: true }],
  ]);
});
test("schema-only null plans and schema drift cannot block record tombstones", async () => {
  const C = AgentCookieCodec,
    A = AgentCookieStorageApplier,
    base = { origin: location.origin, nonce: "fixture_nonce_1234" };
  const database = {
    name: C.encode("drift-delete"),
    version: 1,
    stores: [
      {
        name: C.encode("s"),
        keyPath: null,
        autoIncrement: false,
        indexes: [
          {
            name: C.encode("u"),
            keyPath: C.encode("u"),
            unique: true,
            multiEntry: false,
          },
        ],
        records: [
          { key: C.encodeKey("owned"), value: C.encode({ u: "value" }) },
        ],
      },
    ],
  };
  await A.indexedDB({ ...base, database });
  const empty = await A.readIndexedDBBatch({ ...base, database, stores: null });
  assert.equal(empty.schema, true);
  assert.deepEqual(empty.stores, []);
  const db = await new Promise((resolve, reject) => {
    const r = indexedDB.open("drift-delete", 2);
    r.onupgradeneeded = () => {
      const s = r.transaction.objectStore("s");
      s.deleteIndex("u");
      s.createIndex("u", "different", { unique: true });
    };
    r.onsuccess = () => resolve(r.result);
    r.onerror = () => reject(r.error);
  });
  db.close();
  const stores = [{ name: C.encode("s"), keys: [C.encodeKey("owned")] }];
  const before = await A.readIndexedDBBatch({ ...base, database, stores });
  assert.equal(before.schema, false);
  assert.equal(before.stores[0][0].absent, false);
  const minimal = { name: database.name, version: 1, stores: null };
  assert.equal(
    (await A.readIndexedDBBatch({ ...base, database: minimal, stores }))
      .stores[0][0].absent,
    false,
  );
  await A.indexedDB({
    ...base,
    database: { ...minimal, stores: [] },
    deletes_only: true,
    deletes: [{ store: C.encode("s"), key: C.encodeKey("owned") }],
  });
  assert.equal(
    (await A.readIndexedDBBatch({ ...base, database, stores })).stores[0][0]
      .absent,
    true,
  );
  const noStores = { name: C.encode("schema-only"), version: 1, stores: [] };
  await A.indexedDB({ ...base, database: noStores });
  assert.equal(
    (await A.readIndexedDBBatch({ ...base, database: noStores, stores: null }))
      .schema,
    true,
  );
});
test("unsupported local value cannot poison another owned key readback", async () => {
  const C = AgentCookieCodec,
    A = AgentCookieStorageApplier,
    base = { origin: location.origin, nonce: "fixture_nonce_1234" };
  const database = {
    name: C.encode("poison-batch"),
    version: 1,
    stores: [
      {
        name: C.encode("s"),
        keyPath: null,
        autoIncrement: false,
        indexes: [],
        records: [{ key: C.encodeKey("auth"), value: C.encode("owned-token") }],
      },
    ],
  };
  await A.indexedDB({ ...base, database });
  const db = await new Promise((r) => {
    const q = indexedDB.open("poison-batch");
    q.onsuccess = () => r(q.result);
  });
  await new Promise((resolve, reject) => {
    const tx = db.transaction("s", "readwrite");
    tx.objectStore("s").put(new Blob(["local"]), "unowned");
    tx.oncomplete = resolve;
    tx.onabort = () => reject(tx.error);
  });
  db.close();
  const out = await A.readIndexedDBBatch({
    ...base,
    database,
    stores: [
      {
        name: C.encode("s"),
        keys: [C.encodeKey("unowned"), C.encodeKey("auth")],
      },
    ],
  });
  assert.equal(out.stores[0][0].error, "unsupported_value");
  assert.deepEqual(out.stores[0][1], {
    absent: false,
    value: C.encode("owned-token"),
  });
  await A.indexedDB({
    ...base,
    database: { name: database.name, version: 1, stores: [] },
    deletes_only: true,
    deletes: [{ store: C.encode("s"), key: C.encodeKey("auth") }],
  });
});
