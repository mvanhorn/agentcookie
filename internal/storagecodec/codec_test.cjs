const test = require("node:test");
const assert = require("node:assert/strict");
require("./codec.js");
const C = globalThis.AgentCookieCodec;
test("every supported type and reference survives a canonical round trip", () => {
  const buffer = new ArrayBuffer(32);
  const view = new Uint16Array(buffer, 4, 3);
  view.set([1, 65535, 4]);
  const value = {
    nil: null,
    undef: undefined,
    bool: true,
    nums: [0, -0, NaN, Infinity, -Infinity, 2.5],
    bigint: 999999999999999999999n,
    str: "a😀",
    lone: "x\ud800y\udfff",
    date: new Date(1234),
    invalidDate: new Date(NaN),
    re: /x/giu,
    buffer,
    view,
    dv: new DataView(buffer, 2, 7),
    boxed: [
      new Number(-0),
      new String("x\ud800"),
      new Boolean(false),
      Object(3n),
    ],
  };
  value.array = new Array(5);
  value.array[2] = undefined;
  value.array[4] = value;
  value.map = new Map([[value, value.array]]);
  value.set = new Set([value, value.map]);
  value.self = value;
  const wire = C.encode(value);
  const out = C.decode(wire);
  assert.deepEqual(out, value);
  assert.equal(out.view.buffer, out.buffer);
  assert.equal(out.dv.buffer, out.buffer);
  assert.equal(out.self, out);
  assert.equal(0 in out.array, false);
  assert.equal(2 in out.array, true);
  assert.equal(C.canonical(out), JSON.stringify(wire));
});
test("all typed array kinds round trip", () => {
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
  ]) {
    if (typeof globalThis[kind] !== "function") continue;
    const v = new globalThis[kind](3);
    v[1] = kind.startsWith("Big") ? 9n : 9;
    assert.deepEqual(C.decode(C.encode(v)), v);
  }
});
test("property names preserve UTF16 and cannot pollute prototypes", () => {
  const v = {};
  Object.defineProperty(v, "__proto__", {
    value: { polluted: true },
    enumerable: true,
  });
  v["\ud800"] = 4;
  const o = C.decode(C.encode(v));
  assert.equal(Object.getPrototypeOf(o), Object.prototype);
  assert.equal(o.__proto__.polluted, true);
  assert.equal({}.polluted, undefined);
  assert.equal(o["\ud800"], 4);
});
test("rejects unsupported values with named reasons", () => {
  for (const v of [
    new Blob(["x"]),
    new Error("x"),
    () => {},
    Symbol("x"),
    new SharedArrayBuffer(2),
    new URL("https://example.com"),
  ])
    assert.throws(() => C.encode(v), /unsupported/);
  const v = {};
  Object.defineProperty(v, "x", {
    enumerable: true,
    get() {
      throw Error("must not run");
    },
  });
  assert.throws(() => C.encode(v), /accessor/);
});
test("bounds both directions and rejects malformed graphs", () => {
  let v = 1;
  for (let i = 0; i < 66; i++) v = [v];
  assert.throws(() => C.encode(v), /depth/);
  assert.throws(() => C.encode("12345", { maxBytes: 3 }), /bytes/);
  assert.throws(() => C.encode([1, 2, 3], { maxNodes: 2 }), /nodes/);
  for (const v of [
    { t: "ref", id: 1 },
    { t: "arr", id: 1, len: 2 ** 32, items: [] },
    { t: "buf", id: 1, b64: "?bad" },
    {
      t: "obj",
      id: 1,
      props: [
        [
          { t: "str", v: "x" },
          { t: "ref", id: 2 },
        ],
      ],
    },
    { t: "str16", v: [65536] },
    { t: "num", v: Infinity },
    {
      t: "view",
      id: 1,
      kind: "Function",
      buf: { t: "buf", id: 2, b64: "" },
      off: 0,
      len: 0,
    },
  ])
    assert.throws(() => C.decode(v));
  assert.throws(
    () => C.decode({ t: "arr", id: 1, len: 100, items: [] }, { maxBytes: 10 }),
    /bytes/,
  );
});
test("keys are a restricted subset with no cycles or NaN", () => {
  for (const v of [1, new Date(2), "x\ud800", new Uint8Array([1, 2]), [1, "x"]])
    assert.deepEqual(C.decode(C.encodeKey(v)), v);
  for (const v of [NaN, undefined, null, true, {}, new Date(NaN), 1n])
    assert.throws(() => C.encodeKey(v), /key/);
  const a = [];
  a.push(a);
  assert.throws(() => C.encodeKey(a), /key/);
});
test("JSON byte accounting includes multibyte UTF8, lone surrogates, schema and escaping", () => {
  for (const v of [
    {
      name: C.encode("é漢😀\ud800"),
      stores: [{ name: C.encode("索引"), indexes: [], records: [] }],
    },
    [1, true, null, '\n"\\'],
    { x: undefined, y: "a" },
  ]) {
    const bytes = Buffer.byteLength(JSON.stringify(v));
    assert.equal(C.jsonBytes(v), bytes);
    assert.throws(() => C.jsonBytes(v, bytes - 1), /bytes/);
    assert.equal(C.jsonBytes(v, bytes), bytes);
  }
});
test("near-cap binary encode and decode use the same wire budget", () => {
  const input = new Uint8Array(11 * 1024 * 1024);
  input[0] = 42;
  input[input.length - 1] = 99;
  const encoded = C.encode(input.buffer);
  assert.ok(C.jsonBytes(encoded) < 16 * 1024 * 1024);
  const output = new Uint8Array(C.decode(encoded));
  assert.equal(output.length, input.length);
  assert.equal(output[0], 42);
  assert.equal(output.at(-1), 99);
  assert.throws(
    () => C.encode(new ArrayBuffer(13 * 1024 * 1024)),
    /bytes limit/,
  );
});
test("wire budgets include IR metadata and permit the full semantic depth", () => {
  let value = "leaf";
  for (let n = 0; n < 64; n++) value = { next: value };
  assert.deepEqual(C.decode(C.encode(value)), value);
  assert.throws(
    () =>
      C.decode(
        { t: "str", v: "x", ignored: "x".repeat(200) },
        { maxBytes: 100 },
      ),
    /bytes limit/,
  );
  assert.throws(
    () => C.decode({ t: "buf", id: 1, b64: "AB==" }),
    /invalid base64/,
  );
  const wire = C.encode(new ArrayBuffer(10));
  const limit = C.jsonBytes(wire);
  assert.throws(
    () => C.encode(new ArrayBuffer(10), { maxBytes: limit - 1 }),
    /bytes limit/,
  );
});
