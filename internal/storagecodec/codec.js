// Shared IR v1 codec. This exact asset runs in the source and destination MAIN worlds.
// IDs are assigned to every object, including Dates and views, preserving aliases.
(() => {
  "use strict";
  const LIMITS = Object.freeze({
    maxDepth: 64,
    maxNodes: 100000,
    maxBytes: 16 * 1024 * 1024,
  });
  const kinds = [
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
    "DataView",
  ];
  const fail = (s) => {
    throw new Error("storage codec: " + s);
  };
  const integer = (n) => Number.isSafeInteger(n) && n >= 0;
  function budget(opts = {}) {
    const b = {};
    for (const k of Object.keys(LIMITS)) {
      b[k] = opts[k] === undefined ? LIMITS[k] : Math.min(opts[k], LIMITS[k]);
      if (!integer(b[k])) fail("invalid limit");
    }
    let bytes = 0,
      nodes = 0;
    return {
      visit(d, n = 16) {
        if (d > b.maxDepth) fail("depth limit");
        if (++nodes > b.maxNodes) fail("nodes limit");
        this.add(n);
      },
      add(n) {
        bytes += n;
        if (!Number.isSafeInteger(bytes) || bytes > b.maxBytes)
          fail("bytes limit");
      },
      max: b.maxBytes,
    };
  }
  function stringSize(s) {
    let n = 0;
    for (let i = 0; i < s.length; i++) {
      const c = s.charCodeAt(i);
      if (c < 32)
        n += c === 8 || c === 9 || c === 10 || c === 12 || c === 13 ? 2 : 6;
      else if (c === 34 || c === 92) n += 2;
      else if (c < 128) n++;
      else if (c < 2048) n += 2;
      else if (
        c >= 0xd800 &&
        c <= 0xdbff &&
        i + 1 < s.length &&
        s.charCodeAt(i + 1) >= 0xdc00 &&
        s.charCodeAt(i + 1) <= 0xdfff
      ) {
        n += 4;
        i++;
      } else n += 3;
    }
    return n + 16;
  }
  function string(s, b) {
    b.add(s.isWellFormed() ? stringSize(s) : s.length * 6 + 16);
    if (s.isWellFormed()) return { t: "str", v: s };
    return {
      t: "str16",
      v: Array.from({ length: s.length }, (_, i) => s.charCodeAt(i)),
    };
  }
  function readString(v, b) {
    if (!v || typeof v !== "object") fail("invalid string");
    if (v.t === "str" && typeof v.v === "string" && v.v.isWellFormed()) {
      b.add(stringSize(v.v));
      return v.v;
    }
    if (v.t === "str16" && Array.isArray(v.v)) {
      b.add(v.v.length * 6 + 16);
      let out = "";
      for (let i = 0; i < v.v.length; i += 4096) {
        const a = v.v.slice(i, i + 4096);
        if (!a.every((n) => integer(n) && n <= 65535)) fail("invalid UTF16");
        out += String.fromCharCode(...a);
      }
      return out;
    }
    fail("invalid string");
  }
  function encode(value, opts) {
    const b = budget(opts),
      seen = new Map();
    function enc(v, d) {
      b.visit(d);
      if (v === null) return { t: "null" };
      switch (typeof v) {
        case "undefined":
          return { t: "undef" };
        case "boolean":
          return { t: "bool", v };
        case "number":
          return Number.isFinite(v) && !Object.is(v, -0)
            ? { t: "num", v }
            : { t: "numx", v: Object.is(v, -0) ? "-0" : String(v) };
        case "bigint": {
          const s = String(v);
          b.add(s.length);
          return { t: "bigint", v: s };
        }
        case "string":
          return string(v, b);
        case "function":
        case "symbol":
          fail("unsupported " + typeof v);
      }
      if (seen.has(v)) return { t: "ref", id: seen.get(v) };
      const id = seen.size + 1;
      seen.set(v, id);
      if (v instanceof Date)
        return {
          t: "date",
          id,
          v: Number.isNaN(v.getTime()) ? "NaN" : v.getTime(),
        };
      if (v instanceof RegExp)
        return { t: "regexp", id, src: string(v.source, b), flags: v.flags };
      if (v instanceof ArrayBuffer) {
        if (v.resizable) fail("unsupported ResizableArrayBuffer");
        b.add(4 * Math.ceil(v.byteLength / 3));
        let bin = "";
        const a = new Uint8Array(v);
        for (let i = 0; i < a.length; i += 8192)
          bin += String.fromCharCode(...a.subarray(i, i + 8192));
        return { t: "buf", id, b64: btoa(bin) };
      }
      if (ArrayBuffer.isView(v)) {
        if (!(v.buffer instanceof ArrayBuffer))
          fail("unsupported SharedArrayBuffer");
        const kind = v.constructor.name;
        if (
          !kinds.includes(kind) ||
          Object.getPrototypeOf(v) !== globalThis[kind].prototype
        )
          fail("unsupported view");
        return {
          t: "view",
          id,
          kind,
          buf: enc(v.buffer, d + 1),
          off: v.byteOffset,
          len: kind === "DataView" ? v.byteLength : v.length,
        };
      }
      if (v instanceof Map)
        return {
          t: "map",
          id,
          entries: Array.from(v, ([k, val]) => [
            enc(k, d + 1),
            enc(val, d + 1),
          ]),
        };
      if (v instanceof Set)
        return {
          t: "set",
          id,
          entries: Array.from(v, (val) => enc(val, d + 1)),
        };
      for (const kind of ["Number", "String", "Boolean", "BigInt"])
        if (Object.getPrototypeOf(v) === globalThis[kind].prototype)
          return { t: "boxed", id, kind, v: enc(v.valueOf(), d + 1) };
      if (Array.isArray(v)) {
        b.add(v.length);
        const items = [];
        for (const k of Object.keys(v)) {
          if (!/^(0|[1-9][0-9]*)$/.test(k) || +k >= v.length)
            fail("unsupported array property");
          const desc = Object.getOwnPropertyDescriptor(v, k);
          if (!("value" in desc)) fail("unsupported accessor");
          items.push([+k, enc(desc.value, d + 1)]);
        }
        return { t: "arr", id, len: v.length, items };
      }
      if (
        Object.getPrototypeOf(v) !== Object.prototype &&
        Object.getPrototypeOf(v) !== null
      )
        fail("unsupported " + Object.prototype.toString.call(v));
      if (Object.getOwnPropertySymbols(v).length)
        fail("unsupported symbol property");
      const props = [];
      for (const k of Object.keys(v)) {
        const desc = Object.getOwnPropertyDescriptor(v, k);
        if (!("value" in desc)) fail("unsupported accessor");
        props.push([string(k, b), enc(desc.value, d + 1)]);
      }
      return { t: "obj", id, props };
    }
    const encoded = enc(value, 0);
    jsonBytes(encoded, b.max);
    return encoded;
  }
  function decode(value, opts) {
    const b = budget(opts),
      refs = new Map();
    jsonBytes(value, b.max);
    function register(v, o) {
      if (
        !integer(v.id) ||
        v.id === 0 ||
        refs.has(v.id) ||
        v.id !== refs.size + 1
      )
        fail("invalid object id");
      refs.set(v.id, o);
      return o;
    }
    function list(v) {
      if (!Array.isArray(v)) fail("invalid list");
      return v;
    }
    function dec(v, d) {
      b.visit(d);
      if (!v || typeof v !== "object" || Array.isArray(v))
        fail("invalid value");
      switch (v.t) {
        case "null":
          return null;
        case "undef":
          return undefined;
        case "bool":
          if (typeof v.v !== "boolean") fail("invalid bool");
          return v.v;
        case "num":
          if (
            typeof v.v !== "number" ||
            !Number.isFinite(v.v) ||
            Object.is(v.v, -0)
          )
            fail("invalid num");
          return v.v;
        case "numx":
          if (!["NaN", "Infinity", "-Infinity", "-0"].includes(v.v))
            fail("invalid numx");
          return Number(v.v);
        case "bigint":
          if (typeof v.v !== "string" || !/^-?(0|[1-9][0-9]*)$/.test(v.v))
            fail("invalid bigint");
          b.add(v.v.length);
          return BigInt(v.v);
        case "str":
        case "str16":
          return readString(v, b);
        case "ref":
          if (!refs.has(v.id) || refs.get(v.id) === null)
            fail("invalid reference");
          return refs.get(v.id);
        case "date":
          if (
            v.v !== "NaN" &&
            (typeof v.v !== "number" ||
              !Number.isFinite(v.v) ||
              Math.abs(v.v) > 8640000000000000)
          )
            fail("invalid date");
          return register(v, new Date(v.v === "NaN" ? NaN : v.v));
        case "regexp": {
          const s = readString(v.src, b);
          if (typeof v.flags !== "string" || v.flags.length > 10)
            fail("invalid regexp");
          return register(v, new RegExp(s, v.flags));
        }
        case "buf": {
          if (typeof v.b64 !== "string") fail("invalid buffer");
          b.add(v.b64.length);
          // Scan instead of a nested repetition regexp: near-cap buffers must
          // not overflow the regexp engine stack.
          const padding = v.b64.endsWith("==")
            ? 2
            : v.b64.endsWith("=")
              ? 1
              : 0;
          if (v.b64.length % 4) fail("invalid base64");
          for (let i = 0; i < v.b64.length - padding; i++) {
            const c = v.b64.charCodeAt(i);
            if (
              !(
                (c >= 65 && c <= 90) ||
                (c >= 97 && c <= 122) ||
                (c >= 48 && c <= 57) ||
                c === 43 ||
                c === 47
              )
            )
              fail("invalid base64");
          }
          if (padding) {
            const alphabet =
              "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
            const last = alphabet.indexOf(v.b64[v.b64.length - padding - 1]);
            if (last < 0 || last % (padding === 2 ? 16 : 4))
              fail("invalid base64");
          }
          const bin = atob(v.b64);
          const a = new Uint8Array(bin.length);
          for (let i = 0; i < bin.length; i++) a[i] = bin.charCodeAt(i);
          return register(v, a.buffer);
        }
        case "view": {
          if (!kinds.includes(v.kind) || !integer(v.off) || !integer(v.len))
            fail("invalid view");
          register(v, null);
          const buf = dec(v.buf, d + 1);
          if (!(buf instanceof ArrayBuffer)) fail("invalid view buffer");
          const C = globalThis[v.kind];
          if (typeof C !== "function") fail("unsupported view runtime");
          const size = C.BYTES_PER_ELEMENT || 1;
          if (v.off % size || v.off + v.len * size > buf.byteLength)
            fail("view bounds");
          const o = new C(buf, v.off, v.len);
          refs.set(v.id, o);
          return o;
        }
        case "boxed": {
          if (!["Number", "String", "Boolean", "BigInt"].includes(v.kind))
            fail("invalid boxed");
          register(v, null);
          const p = dec(v.v, d + 1);
          if (typeof p !== v.kind.toLowerCase()) fail("invalid boxed value");
          const o = Object(p);
          refs.set(v.id, o);
          return o;
        }
        case "arr": {
          if (!integer(v.len) || v.len > 4294967295)
            fail("invalid array length");
          b.add(v.len);
          const a = register(v, new Array(v.len));
          let last = -1;
          for (const pair of list(v.items)) {
            if (
              !Array.isArray(pair) ||
              pair.length !== 2 ||
              !integer(pair[0]) ||
              pair[0] <= last ||
              pair[0] >= v.len
            )
              fail("invalid array index");
            last = pair[0];
            a[pair[0]] = dec(pair[1], d + 1);
          }
          return a;
        }
        case "obj": {
          const o = register(v, {});
          for (const pair of list(v.props)) {
            if (!Array.isArray(pair) || pair.length !== 2)
              fail("invalid property");
            const k = readString(pair[0], b);
            if (Object.hasOwn(o, k)) fail("duplicate property");
            Object.defineProperty(o, k, {
              value: dec(pair[1], d + 1),
              writable: true,
              enumerable: true,
              configurable: true,
            });
          }
          return o;
        }
        case "map": {
          const m = register(v, new Map());
          for (const p of list(v.entries)) {
            if (!Array.isArray(p) || p.length !== 2) fail("invalid map entry");
            m.set(dec(p[0], d + 1), dec(p[1], d + 1));
          }
          return m;
        }
        case "set": {
          const s = register(v, new Set());
          for (const p of list(v.entries)) s.add(dec(p, d + 1));
          return s;
        }
        default:
          fail("unsupported tag " + String(v.t));
      }
    }
    return dec(value, 0);
  }
  function encodeKey(value, opts) {
    const seen = new Set();
    function key(v, d) {
      if (d > 64) fail("key depth");
      if (typeof v === "number" && !Number.isNaN(v)) return;
      if (typeof v === "string") return;
      if (v instanceof Date && !Number.isNaN(v.getTime())) return;
      if (
        v instanceof ArrayBuffer ||
        (ArrayBuffer.isView(v) && v.buffer instanceof ArrayBuffer)
      )
        return;
      if (Array.isArray(v) && !seen.has(v)) {
        seen.add(v);
        for (let i = 0; i < v.length; i++) {
          if (!(i in v)) fail("invalid key hole");
          key(v[i], d + 1);
        }
        seen.delete(v);
        return;
      }
      fail("invalid key");
    }
    key(value, 0);
    return encode(value, opts);
  }
  // Counts JSON wire bytes without allocating a second UTF8 buffer. The IR is
  // acyclic JSON even when its represented value contains cycles.
  function jsonBytes(value, max = 128 * 1024 * 1024) {
    let size = 0;
    const add = (n) => {
      size += n;
      if (size > max) fail("bytes limit");
    };
    function quoted(s) {
      add(2);
      for (let i = 0; i < s.length; i++) {
        const c = s.charCodeAt(i);
        if (c < 32)
          add(c === 8 || c === 9 || c === 10 || c === 12 || c === 13 ? 2 : 6);
        else if (c === 34 || c === 92) add(2);
        else if (c < 128) add(1);
        else if (c < 2048) add(2);
        else if (c >= 0xd800 && c <= 0xdbff) {
          if (
            i + 1 < s.length &&
            s.charCodeAt(i + 1) >= 0xdc00 &&
            s.charCodeAt(i + 1) <= 0xdfff
          ) {
            add(4);
            i++;
          } else add(6);
        } else if (c >= 0xdc00 && c <= 0xdfff) add(6);
        else add(3);
      }
    }
    function walk(v, d) {
      if (d > 256) fail("JSON depth limit");
      if (v === null) {
        add(4);
        return;
      }
      switch (typeof v) {
        case "string":
          quoted(v);
          return;
        case "boolean":
          add(v ? 4 : 5);
          return;
        case "number":
          if (!Number.isFinite(v)) fail("invalid JSON number");
          add(String(v).length);
          return;
        case "object":
          break;
        default:
          fail("invalid JSON value");
      }
      if (Array.isArray(v)) {
        add(2);
        for (let i = 0; i < v.length; i++) {
          if (i) add(1);
          walk(v[i], d + 1);
        }
      } else {
        add(2);
        let n = 0;
        for (const k of Object.keys(v)) {
          if (v[k] === undefined) continue;
          if (n++) add(1);
          quoted(k);
          add(1);
          walk(v[k], d + 1);
        }
      }
    }
    walk(value, 0);
    return size;
  }
  globalThis.AgentCookieCodec = Object.freeze({
    encode,
    decode,
    encodeKey,
    jsonBytes,
    canonical: (v, opts) => JSON.stringify(encode(v, opts)),
    limits: LIMITS,
  });
})();
