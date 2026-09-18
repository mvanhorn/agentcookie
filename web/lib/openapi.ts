// OpenAPI 3.1 description of the sink HTTP interface, as a typed
// object. /openapi.json serves it verbatim.
//
// agentcookie.dev hosts no API. The three operations below are
// served by the agentcookie binary on the user's own machines inside
// their Tailscale tailnet: /healthz and /sync by `agentcookie sink`
// (internal/cli/sink.go, default port 9999) and /pair by the
// temporary source-side listener that `agentcookie pair --as source`
// runs for at most ten minutes (internal/pairing/pairing.go, default
// port 9998). Every schema here is transcribed from the Go types:
// protocol.SyncEnvelope, chrome.Cookie, pairing.SinkRequest and
// pairing.SourceResponse. Where the wire body is an AES-GCM seal the
// schema says binary rather than guessing fields.
//
// Every absolute URL comes from SITE_ORIGIN or the links module.

import { GITHUB, ISSUES } from "./content/links";
import { SITE_ORIGIN } from "./site";

// A deliberately small subset of the OpenAPI 3.1 object model: only
// the keywords this document uses. Enough for the compiler to catch a
// typo in a keyword and for tests to walk the tree with types.

export type JsonSchema = {
  type?: "object" | "string" | "integer" | "array" | "boolean";
  description?: string;
  properties?: Readonly<Record<string, JsonSchema>>;
  required?: readonly string[];
  additionalProperties?: JsonSchema | boolean;
  items?: JsonSchema;
  format?: string;
  contentEncoding?: "base64";
  pattern?: string;
  minimum?: number;
  maximum?: number;
  const?: string | number;
  examples?: readonly unknown[];
  $ref?: string;
};

export type MediaTypeObject = {
  schema: JsonSchema;
};

export type ResponseObject = {
  description: string;
  content: Readonly<Record<string, MediaTypeObject>>;
};

export type RequestBodyObject = {
  description: string;
  required: true;
  content: Readonly<Record<string, MediaTypeObject>>;
};

export type ServerVariable = {
  default: string;
  description: string;
};

export type ServerObject = {
  url: string;
  description: string;
  variables?: Readonly<Record<string, ServerVariable>>;
};

export type OperationObject = {
  operationId: string;
  summary: string;
  description: string;
  requestBody?: RequestBodyObject;
  responses: Readonly<Record<string, ResponseObject>>;
  // Always empty: there is no HTTP-level credential. Trust comes from
  // the tailnet and the pairing-derived key; see info.description.
  security: readonly [];
};

export type PathItemObject = {
  summary: string;
  description: string;
  servers?: readonly ServerObject[];
  get?: OperationObject;
  post?: OperationObject;
};

export type OpenApiDocument = {
  openapi: "3.1.0";
  info: {
    title: string;
    version: string;
    summary: string;
    description: string;
    contact: { name: string; url: string };
    license: { name: string; identifier: string; url: string };
  };
  externalDocs: { description: string; url: string };
  servers: readonly ServerObject[];
  security: readonly [];
  paths: Readonly<Record<string, PathItemObject>>;
  components: {
    schemas: Readonly<Record<string, JsonSchema>>;
  };
};

// ---------------------------------------------------------------
// Servers

const SINK_SERVER: ServerObject = {
  url: "http://{host}:{port}",
  description:
    "The sink listener started by `agentcookie sink` on the machine that receives sessions. `listen.addr` in the sink config names the bind address; the sink refuses to bind anything but a Tailscale 100.x address or explicit loopback. Reach it by the sink's MagicDNS name.",
  variables: {
    host: {
      default: "my-sink.tailnet.ts.net",
      description:
        "The sink's Tailscale MagicDNS hostname (or its 100.x tailnet IP).",
    },
    port: {
      default: "9999",
      description: "The port in the sink config's listen.addr. The quickstart uses 9999.",
    },
  },
};

const PAIR_SERVER: ServerObject = {
  url: "http://{host}:{port}",
  description:
    "The temporary pairing listener that `agentcookie pair --as source` starts on the source Mac. It lives for at most ten minutes, shuts down after the first successful handshake, and binds the machine's Tailscale 100.x address (or the --listen flag). Reach it by the source's MagicDNS name.",
  variables: {
    host: {
      default: "my-laptop.tailnet.ts.net",
      description:
        "The source's Tailscale MagicDNS hostname (or its 100.x tailnet IP).",
    },
    port: {
      default: "9998",
      description: "9998 unless --listen names another port.",
    },
  },
};

// ---------------------------------------------------------------
// Schemas (transcribed from Go types)

const PLAIN_TEXT_ERROR: JsonSchema = {
  type: "string",
  description:
    "One line of text from Go's http.Error, terminated by a newline. The server sets Content-Type: text/plain; charset=utf-8 and X-Content-Type-Options: nosniff on every error.",
};

function textError(description: string, examples: readonly string[]): ResponseObject {
  return {
    description,
    content: {
      "text/plain": {
        schema: { ...PLAIN_TEXT_ERROR, examples },
      },
    },
  };
}

// internal/chrome/read.go: type Cookie. Every field is copied
// verbatim from the corresponding column of Chrome's Cookies SQLite
// database; the integer flags carry Chrome's own 0/1 and enum values.
const COOKIE_SCHEMA: JsonSchema = {
  type: "object",
  description:
    "One Chrome cookie as the source read it (Go type chrome.Cookie). Field names and values mirror the columns of Chrome's Cookies SQLite database; `value` is the decrypted cookie value.",
  required: [
    "host_key",
    "name",
    "value",
    "path",
    "expires_utc",
    "is_secure",
    "is_httponly",
    "last_access_utc",
    "has_expires",
    "is_persistent",
    "priority",
    "samesite",
    "source_scheme",
    "source_port",
  ],
  properties: {
    host_key: { type: "string", description: "Cookie domain as Chrome stores it, for example `.example.com`." },
    name: { type: "string" },
    value: { type: "string", description: "Decrypted cookie value." },
    path: { type: "string" },
    expires_utc: { type: "integer", format: "int64", description: "Chrome's expires_utc column." },
    is_secure: { type: "integer", description: "0 or 1." },
    is_httponly: { type: "integer", description: "0 or 1." },
    last_access_utc: { type: "integer", format: "int64", description: "Chrome's last_access_utc column." },
    has_expires: { type: "integer", description: "0 or 1." },
    is_persistent: { type: "integer", description: "0 or 1." },
    priority: { type: "integer", description: "Chrome's priority enum." },
    samesite: { type: "integer", description: "Chrome's samesite enum." },
    source_scheme: { type: "integer", description: "Chrome's source_scheme enum." },
    source_port: { type: "integer" },
  },
};

// internal/protocol/envelope.go: type SyncEnvelope.
const SYNC_ENVELOPE_SCHEMA: JsonSchema = {
  type: "object",
  description:
    "The JSON document inside the AES-256-GCM seal of a POST /sync body (Go type protocol.SyncEnvelope). Never sent in the clear. cookies, local_storage_tarball, indexed_db_tarball, and secrets are each optional on protocol version 2; the sink applies whatever is present.",
  required: ["protocol_version", "source_hostname", "sequence", "cookies"],
  properties: {
    protocol_version: {
      type: "integer",
      minimum: 1,
      maximum: 2,
      description:
        "Wire protocol version. The sink accepts 1 (cookies only) and 2 (adds localStorage, IndexedDB, and secrets); sources always emit 2.",
    },
    source_hostname: {
      type: "string",
      description: "The source's announced hostname. The replay tracker keys its last-seen sequence on this value.",
    },
    sequence: {
      type: "integer",
      format: "int64",
      description:
        "Monotonic per-source counter. The sink rejects an envelope whose sequence is not greater than the last one it accepted from the same source_hostname, and it persists that watermark across restarts.",
    },
    cookies: {
      type: "array",
      items: { $ref: "#/components/schemas/Cookie" },
      description: "Full-set semantics: every cookie the source's policy allows, not a diff.",
    },
    local_storage_tarball: {
      type: "string",
      contentEncoding: "base64",
      description:
        "Optional. Bytes produced by the source's chromedirsync packer for Chrome's Local Storage leveldb; the sink unpacks and atomically replaces the directory in its Default profile.",
    },
    indexed_db_tarball: {
      type: "string",
      contentEncoding: "base64",
      description: "Optional. Same packing as local_storage_tarball, for Chrome's IndexedDB directory.",
    },
    indexed_db_skipped: {
      type: "array",
      items: { type: "string" },
      description: "Optional. IndexedDB origins the source skipped.",
    },
    secrets: {
      type: "object",
      description:
        "Optional secrets-bus payload (docs/spec-agentcookie-secrets-bus-v1.md): one map per registered CLI, keyed by CLI name, of KEY to value pairs that pass the manifest's sync policy. The sink writes each map to ~/.agentcookie/secrets/<cli>/secrets.env.",
      additionalProperties: {
        type: "object",
        additionalProperties: { type: "string" },
      },
    },
  },
};

// internal/pairing/pairing.go: type SinkRequest.
const PAIR_REQUEST_SCHEMA: JsonSchema = {
  type: "object",
  description: "The sink's half of the handshake (Go type pairing.SinkRequest).",
  required: ["protocol_version", "code", "sink_public_key", "sink_hostname"],
  properties: {
    protocol_version: {
      type: "integer",
      const: 1,
      description: "Pairing protocol version. The source rejects anything but 1.",
    },
    code: {
      type: "string",
      pattern: "^[A-Z2-7]{4}-[A-Z2-7]{4}-[A-Z2-7]{4}$",
      description:
        "The 12-character base32 pairing code the source printed, in XXXX-XXXX-XXXX form. Compared in constant time after normalizing case and separators.",
      examples: ["YILU-OIVK-Q7ZA"],
    },
    sink_public_key: {
      type: "string",
      contentEncoding: "base64",
      description: "The sink's ephemeral X25519 public key, 32 bytes, base64 as Go encodes a byte slice.",
    },
    sink_hostname: {
      type: "string",
      description: "The sink's hostname; the source records it as the paired peer.",
    },
  },
};

// internal/pairing/pairing.go: type SourceResponse.
const PAIR_RESPONSE_SCHEMA: JsonSchema = {
  type: "object",
  description: "The source's half of the handshake (Go type pairing.SourceResponse).",
  required: ["protocol_version", "source_public_key", "source_hostname", "fingerprint"],
  properties: {
    protocol_version: { type: "integer", const: 1 },
    source_public_key: {
      type: "string",
      contentEncoding: "base64",
      description: "The source's ephemeral X25519 public key, 32 bytes, base64.",
    },
    source_hostname: { type: "string" },
    fingerprint: {
      type: "string",
      pattern: "^[0-9a-f]{8}$",
      description:
        "First four bytes of SHA-256 over the derived key, hex. Both sides print it so the operator can confirm they derived the same key.",
    },
  },
};

// ---------------------------------------------------------------
// Operations

const HEALTH: OperationObject = {
  operationId: "health",
  summary: "Liveness check",
  description:
    "Returns the literal line `ok`. The handler does not inspect the method, headers, or body; it exists so an operator or a probe can tell that the sink process is up and bound. It says nothing about pairing state or Chrome.",
  responses: {
    "200": {
      description: "The sink is listening.",
      content: {
        "text/plain": {
          schema: { type: "string", const: "ok\n" },
        },
      },
    },
  },
  security: [],
};

const SYNC: OperationObject = {
  operationId: "sync",
  summary: "Deliver one sealed sync envelope",
  description: [
    "The source POSTs the raw bytes of an AES-256-GCM seal: a 12-byte random nonce followed by the ciphertext and tag of a SyncEnvelope JSON document (see components.schemas.SyncEnvelope). The AES key is SHA-256 of the pairing-derived shared key, so only the peer that completed the pairing handshake can produce a body the sink will open. The source sends it as application/octet-stream with no other headers; the sink ignores the Content-Type.",
    "On success the sink decrypts, checks the protocol version, loads its own cookie policy, checks the sequence against the last one seen from that source_hostname, filters the cookies through its blocklist, writes cookies, localStorage, IndexedDB, and secrets, runs its adapters, and replies with a one-line summary. The body is capped at 256 MiB.",
  ].join("\n\n"),
  requestBody: {
    description:
      "The sealed envelope. Opaque on the wire: nonce (12 bytes) followed by AES-256-GCM ciphertext and tag over the SyncEnvelope JSON.",
    required: true,
    content: {
      "application/octet-stream": {
        schema: {
          type: "string",
          format: "binary",
          description:
            "AES-256-GCM seal of a SyncEnvelope, nonce prepended. Not decodable without the pairing-derived key.",
        },
      },
    },
  },
  responses: {
    "200": {
      description:
        "The envelope was accepted and applied. One line: `ok: wrote N cookies (N sidecar), N localStorage origins, N indexedDB origins; dropped N <policy> cookies`, with a `; live_cdp: ...` suffix when the Linux live CDP path ran. A sink started with --dry-run replies `dry-run ok: accepted N cookies; dropped N <policy> cookies` and writes nothing.",
      content: {
        "text/plain": {
          schema: {
            type: "string",
            examples: [
              "ok: wrote 412 cookies (412 sidecar), 3 localStorage origins, 1 indexedDB origins; dropped 2 blocklisted cookies\n",
              "dry-run ok: accepted 412 cookies; dropped 2 blocklisted cookies\n",
            ],
          },
        },
      },
    },
    "400": textError(
      "The body could not be read, the decrypted JSON did not parse as a SyncEnvelope, or protocol_version is outside the range the sink speaks.",
      [
        "read body: unexpected EOF\n",
        "unmarshal envelope: invalid character 'x' looking for beginning of value\n",
        "protocol version mismatch: got 3, sink speaks 1-2\n",
      ],
    ),
    "401": textError(
      "The seal did not open: the body was shorter than a nonce, was sealed under a different key, or was tampered with. This is the only authentication check and it happens before anything is parsed.",
      ["open payload: decrypt (wrong secret or tampered payload): cipher: message authentication failed\n"],
    ),
    "405": textError("Any method other than POST.", ["POST only\n"]),
    "409": textError(
      "Replay defense: the envelope's sequence is not greater than the last one accepted from the same source_hostname.",
      ['sequence 41 not greater than last seen for "my-laptop" (replay defense)\n'],
    ),
    "500": textError(
      "The sink could not load its cookie policy or the write to Chrome, the sidecar, or the secrets bus failed. The failure is recorded in the sink's state file for `agentcookie status`.",
      ["load blocklist: open blocklist.yaml: permission denied\n", "apply envelope: chrome cookies db is locked\n"],
    ),
  },
  security: [],
};

const PAIR: OperationObject = {
  operationId: "pair",
  summary: "Complete the pairing handshake",
  description: [
    "Served by the source, not the sink. `agentcookie pair --as source` generates an ephemeral X25519 keypair and a 12-character pairing code, prints the code, and listens on this endpoint for up to ten minutes. `agentcookie pair --as sink --pair-url <this URL> --code <code>` POSTs the sink's own ephemeral public key together with the code. The source checks the protocol version and the code, replies with its public key and a fingerprint, and both sides derive the same 32-byte key with HKDF-SHA256 over the X25519 shared secret, salted with the code and keyed with the info string `agentcookie-pair-v1`.",
    "The code is the man-in-the-middle defense: a party that intercepts the exchange without the code derives a different key and every later /sync seal fails its tag check. Wrong-code attempts are rate limited per remote address (5 attempts, one refilled every 500 ms). The listener shuts down after the first successful handshake; a second POST gets a connection error, not a response. Bodies are capped at 16 KiB.",
  ].join("\n\n"),
  requestBody: {
    description: "The sink's public key, hostname, and the pairing code.",
    required: true,
    content: {
      "application/json": {
        schema: { $ref: "#/components/schemas/PairRequest" },
      },
    },
  },
  responses: {
    "200": {
      description:
        "The code matched. The source's public key and the fingerprint of the derived key; the sink derives the same key locally and both sides write it to their config directory.",
      content: {
        "application/json": {
          schema: { $ref: "#/components/schemas/PairResponse" },
        },
      },
    },
    "400": textError(
      "The body could not be read or decoded, protocol_version is not 1, or sink_public_key is not a valid X25519 public key.",
      [
        "decode: invalid character 'x' looking for beginning of value\n",
        "protocol mismatch: sink=2 source=1\n",
        "bad sink public key: crypto/ecdh: invalid public key\n",
      ],
    ),
    "401": textError("The pairing code did not match.", ["invalid pairing code\n"]),
    "405": textError("Any method other than POST.", ["POST only\n"]),
    "429": textError(
      "Too many attempts from this remote address within the pairing window.",
      ["too many pairing attempts\n"],
    ),
    "500": textError(
      "The X25519 agreement or the HKDF derivation failed on the source.",
      ["ecdh: crypto/ecdh: private key and public key curves do not match\n"],
    ),
  },
  security: [],
};

// ---------------------------------------------------------------
// The document

export const OPENAPI_TITLE = "agentcookie sink HTTP interface";

export const OPENAPI: OpenApiDocument = {
  openapi: "3.1.0",
  info: {
    title: OPENAPI_TITLE,
    version: "1.0.0",
    summary:
      "The HTTP interface of the agentcookie sink and pairing listeners, which run on your own machines inside your Tailscale tailnet.",
    description: [
      `This API is not hosted at ${SITE_ORIGIN}. There is no service, no API key, and no sign-up on this domain; requests under ${SITE_ORIGIN}/api answer 404. The operations described here are served by the agentcookie binary you run yourself: /healthz and /sync by \`agentcookie sink\` on the machine that receives sessions (the sink), and /pair by the short-lived listener that \`agentcookie pair --as source\` runs on the machine that owns the Chrome profile (the source). Both listeners bind only a Tailscale 100.x address (or explicit loopback), so the only clients that can reach them are devices on your tailnet.`,
      "Trust does not come from an HTTP credential, which is why every operation declares an empty security requirement. It comes from two layers: the tailnet decides who can open a connection at all, and the pairing handshake gives each source-sink pair its own 32-byte key (X25519 agreement, HKDF-SHA256 salted with a one-time pairing code). Every /sync body is sealed with AES-256-GCM under that key and the sink rejects anything it cannot open, plus anything whose sequence number it has already seen. Cookie policy is enforced on both sides.",
      "Wire formats are transcribed from the Go source: internal/cli/sink.go, internal/pairing/pairing.go, internal/protocol/envelope.go, internal/chrome/read.go, and internal/transport/crypto.go. Where a body is a seal the schema says so instead of listing fields you cannot see on the wire.",
    ].join("\n\n"),
    contact: {
      name: "agentcookie issues on GitHub",
      url: ISSUES,
    },
    license: {
      name: "MIT",
      identifier: "MIT",
      url: `${GITHUB}/blob/main/LICENSE`,
    },
  },
  externalDocs: {
    description: "Developer notes on agentcookie.dev: what the sink interface is, where it runs, and how pairing establishes trust.",
    url: `${SITE_ORIGIN}/developers`,
  },
  servers: [SINK_SERVER],
  security: [],
  paths: {
    "/healthz": {
      summary: "Sink liveness",
      description: "On the sink listener.",
      get: HEALTH,
    },
    "/sync": {
      summary: "Sealed sync envelope delivery",
      description: `On the sink listener. The source's sink.url config value is this path on the sink's MagicDNS name, for example http://${SINK_SERVER.variables?.host.default}:${SINK_SERVER.variables?.port.default}/sync.`,
      post: SYNC,
    },
    "/pair": {
      summary: "Pairing handshake",
      description:
        "On the source's temporary pairing listener, not the sink. The pair URL the source prints is this path on its own MagicDNS name and port 9998.",
      servers: [PAIR_SERVER],
      post: PAIR,
    },
  },
  components: {
    schemas: {
      Cookie: COOKIE_SCHEMA,
      SyncEnvelope: SYNC_ENVELOPE_SCHEMA,
      PairRequest: PAIR_REQUEST_SCHEMA,
      PairResponse: PAIR_RESPONSE_SCHEMA,
    },
  },
};
