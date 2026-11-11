globalThis.AgentCookieStorageFixture = async () => {
  const C = AgentCookieCodec,
    A = AgentCookieStorageApplier,
    R = AgentCookieStorageReader;
  const check = (v, s) => {
    if (!v) throw Error(s);
  };
  const origin = location.origin,
    nonce = "fixture_nonce_1234";
  const rich = {
    date: new Date(123),
    invalid: new Date(NaN),
    regexp: /foo/gi,
    map: new Map([[1, "a"]]),
    set: new Set([2]),
    buf: new Uint8Array([1, 2, 3]).buffer,
    typed: new BigInt64Array([4n]),
    undef: undefined,
    special: [NaN, -0, Infinity, -Infinity],
    big: 12345678901234567890n,
    lone: "\ud800",
    box: Object(12n),
    arr: new Array(3),
  };
  rich.self = rich;
  rich.arr[2] = rich;
  const encoded = C.encode(rich),
    decoded = C.decode(encoded);
  check(C.canonical(decoded) === JSON.stringify(encoded), "codec canonical");
  check(decoded.self === decoded && decoded.arr[2] === decoded, "cycle");
  const store = (name, records = []) => ({
    name: C.encode(name),
    keyPath: null,
    autoIncrement: false,
    indexes: [],
    records,
  });
  const rec = (key, value) => ({
    key: C.encodeKey(key),
    value: C.encode(value),
  });
  const database = {
    name: C.encode("fresh-v1"),
    version: 1,
    stores: [
      store("tokens", [
        rec("owned", rich),
        rec("unowned", "keep"),
        rec("undef", undefined),
      ]),
    ],
  };
  let out = await A.indexedDB({ origin, nonce, database });
  check(out.schema === "created", "fresh v1 creates schema");
  check(
    out.records.length === 3 && out.records.every((r) => !r.absent),
    "readback includes undefined",
  );
  let read = await R.database(
    { name: C.decode(database.name), version: 1 },
    Date.now() + 3000,
  );
  check(read.stores[0].records.length === 3, "reader cursors");
  check(
    read.stores[0].records.find((r) => C.decode(r.key) === "owned").value.t ===
      "obj",
    "reader rich value",
  );
  out = await A.indexedDB({
    origin,
    nonce,
    database: { ...database, stores: [store("tokens", [rec("owned", "new")])] },
    deletes: [{ store: C.encode("tokens"), key: C.encodeKey("undef") }],
  });
  check(
    out.records.some((r) => r.op === "delete" && r.absent),
    "delete readback",
  );
  read = await R.database(
    { name: C.decode(database.name), version: 1 },
    Date.now() + 3000,
  );
  check(read.stores[0].records.length === 2, "unowned retained");
  check(
    read.stores[0].records.some(
      (r) => C.decode(r.key) === "owned" && C.decode(r.value) === "new",
    ),
    "same-key changed value",
  );
  out = await A.indexedDB({
    origin,
    nonce,
    database: { ...database, stores: [store("added")] },
  });
  check(out.schema === "upgraded to 2", "missing schema V+1");
  let versionError = false;
  await new Promise((resolve, reject) => {
    const q = indexedDB.open("fresh-v1", 1);
    q.onerror = () => {
      versionError = q.error.name === "VersionError";
      resolve();
    };
    q.onsuccess = () => {
      q.result.close();
      resolve();
    };
  });
  check(versionError, "lower version throws VersionError");
  let conflict = false;
  try {
    await A.indexedDB({
      origin,
      nonce,
      database: {
        ...database,
        stores: [{ ...store("tokens"), keyPath: C.encode("id") }],
      },
    });
  } catch (e) {
    conflict = e.message.includes("schema conflict");
  }
  check(conflict, "schema mismatch rejected");
  const unique = {
    name: C.encode("atomic"),
    version: 1,
    stores: [
      {
        ...store("s"),
        indexes: [
          {
            name: C.encode("unique"),
            keyPath: C.encode("u"),
            unique: true,
            multiEntry: false,
          },
        ],
      },
    ],
  };
  await A.indexedDB({ origin, nonce, database: unique });
  let rolled = false;
  try {
    await A.indexedDB({
      origin,
      nonce,
      database: {
        ...unique,
        stores: [
          {
            ...unique.stores[0],
            records: [rec(1, { u: "same" }), rec(2, { u: "same" })],
          },
        ],
      },
    });
  } catch {
    rolled = true;
  }
  check(rolled, "unique transaction failed");
  read = await R.database({ name: "atomic", version: 1 }, Date.now() + 3000);
  check(
    read.stores[0].records.length === 0,
    "entire DB transaction rolled back",
  );
  const auto = {
    name: C.encode("auto"),
    version: 1,
    stores: [{ ...store("s", [rec(99, "x")]), autoIncrement: true }],
  };
  await A.indexedDB({ origin, nonce, database: auto });
  read = await R.database({ name: "auto", version: 1 }, Date.now() + 3000);
  check(read.stores[0].generator === 100, "source generator observed");
  read = await R.database({ name: "auto", version: 1 }, Date.now() + 3000);
  check(
    read.stores[0].generator === 100 && read.stores[0].records.length === 1,
    "probe aborted without changing source",
  );
  const key = C.encode("k\ud800"),
    value = C.encode("v\udfff");
  out = A.localStorage({ origin, nonce, op: "set", key, value });
  check(JSON.stringify(out.value) === JSON.stringify(value), "LS UTF16");
  out = A.localStorage({ origin, nonce, op: "delete", key });
  check(out.absent, "LS deletion");
  let rejected = false;
  try {
    A.localStorage({
      origin: "http://wrong.invalid",
      nonce,
      op: "set",
      key,
      value,
    });
  } catch {
    rejected = true;
  }
  check(rejected, "helper identity proof");
  return { checks: 17, browser: navigator.userAgent };
};
