// OpenAPI document contract. The document describes the sink HTTP
// interface that runs on the user's tailnet and says so; every
// operation is described, uniquely named, and unauthenticated at the
// HTTP layer; every response carries a schema; every absolute URL is
// on the production origin or the repository.

import { describe, it, expect } from "vitest";
import {
  OPENAPI,
  OPENAPI_TITLE,
  type JsonSchema,
  type OperationObject,
  type PathItemObject,
} from "./openapi";
import { ISSUES, GITHUB } from "./content/links";

const ORIGIN = "https://agentcookie.dev";

function operations(): { path: string; method: string; op: OperationObject }[] {
  const out: { path: string; method: string; op: OperationObject }[] = [];
  for (const [path, item] of Object.entries(OPENAPI.paths)) {
    for (const method of ["get", "post"] as const) {
      const op = (item as PathItemObject)[method];
      if (op) out.push({ path, method, op });
    }
  }
  return out;
}

function walkSchemas(node: unknown, visit: (schema: JsonSchema) => void): void {
  if (node === null || typeof node !== "object") return;
  if (Array.isArray(node)) {
    for (const item of node) walkSchemas(item, visit);
    return;
  }
  const record = node as Record<string, unknown>;
  if ("schema" in record && record.schema && typeof record.schema === "object") {
    visit(record.schema as JsonSchema);
  }
  for (const value of Object.values(record)) walkSchemas(value, visit);
}

describe("OpenAPI document", () => {
  it("is OpenAPI 3.1.0 with the sink title and an honest description", () => {
    expect(OPENAPI.openapi).toBe("3.1.0");
    expect(OPENAPI.info.title).toBe(OPENAPI_TITLE);
    expect(OPENAPI.info.title).toBe("agentcookie sink HTTP interface");
    expect(OPENAPI.info.description).toContain(`not hosted at ${ORIGIN}`);
    expect(OPENAPI.info.description).toMatch(/tailnet/);
    expect(OPENAPI.info.description).toMatch(/no API key/);
    expect(OPENAPI.info.version.length).toBeGreaterThan(0);
  });

  it("points contact at GitHub issues and externalDocs at /developers", () => {
    expect(OPENAPI.info.contact.url).toBe(ISSUES);
    expect(OPENAPI.externalDocs.url).toBe(`${ORIGIN}/developers`);
    expect(OPENAPI.info.license.url.startsWith(GITHUB)).toBe(true);
  });

  it("has a templated sink server on 9999 and a pairing server on 9998", () => {
    expect(OPENAPI.servers.length).toBeGreaterThan(0);
    const sink = OPENAPI.servers[0];
    expect(sink.url).toBe("http://{host}:{port}");
    expect(sink.variables?.host.default).toBe("my-sink.tailnet.ts.net");
    expect(sink.variables?.port.default).toBe("9999");
    const pair = OPENAPI.paths["/pair"].servers?.[0];
    expect(pair?.url).toBe("http://{host}:{port}");
    expect(pair?.variables?.port.default).toBe("9998");
    expect(pair?.variables?.host.default).toMatch(/\.ts\.net$/);
  });

  it("exposes exactly GET /healthz, POST /sync, and POST /pair", () => {
    expect(
      operations().map(({ method, path }) => `${method.toUpperCase()} ${path}`),
    ).toEqual(["GET /healthz", "POST /sync", "POST /pair"]);
  });

  it("gives every operation a unique operationId, a description, and empty security", () => {
    const ops = operations();
    const ids = ops.map(({ op }) => op.operationId);
    expect(ids).toEqual(["health", "sync", "pair"]);
    expect(new Set(ids).size).toBe(ids.length);
    for (const { op } of ops) {
      expect(op.description.trim().length).toBeGreaterThan(40);
      expect(op.security).toEqual([]);
    }
    expect(OPENAPI.security).toEqual([]);
  });

  it("gives every response a description and a schema for every media type", () => {
    for (const { op, method, path } of operations()) {
      const statuses = Object.keys(op.responses);
      expect(statuses, `${method} ${path}`).toContain("200");
      for (const [status, response] of Object.entries(op.responses)) {
        expect(status).toMatch(/^[1-5]\d\d$/);
        expect(response.description.length, `${path} ${status}`).toBeGreaterThan(0);
        const media = Object.entries(response.content);
        expect(media.length, `${path} ${status}`).toBeGreaterThan(0);
        for (const [, body] of media) {
          expect(body.schema, `${path} ${status}`).toBeDefined();
        }
      }
    }
  });

  it("documents the error statuses the Go handlers actually emit", () => {
    expect(Object.keys(OPENAPI.paths["/sync"].post!.responses).sort()).toEqual(
      ["200", "400", "401", "405", "409", "500"],
    );
    expect(Object.keys(OPENAPI.paths["/pair"].post!.responses).sort()).toEqual(
      ["200", "400", "401", "405", "429", "500"],
    );
    expect(Object.keys(OPENAPI.paths["/healthz"].get!.responses)).toEqual(["200"]);
  });

  it("describes the /sync body as an opaque seal, not as JSON fields", () => {
    const body = OPENAPI.paths["/sync"].post!.requestBody!;
    expect(Object.keys(body.content)).toEqual(["application/octet-stream"]);
    const schema = body.content["application/octet-stream"].schema;
    expect(schema.type).toBe("string");
    expect(schema.format).toBe("binary");
    expect(schema.properties).toBeUndefined();
    expect(body.description).toMatch(/AES-256-GCM/);
  });

  it("transcribes the Go types into component schemas", () => {
    const { Cookie, SyncEnvelope, PairRequest, PairResponse } = OPENAPI.components.schemas;
    expect(Object.keys(Cookie.properties ?? {})).toEqual([
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
    ]);
    expect(Object.keys(SyncEnvelope.properties ?? {})).toEqual([
      "protocol_version",
      "source_hostname",
      "sequence",
      "cookies",
      "local_storage_tarball",
      "indexed_db_tarball",
      "indexed_db_skipped",
      "secrets",
    ]);
    expect(SyncEnvelope.required).toEqual([
      "protocol_version",
      "source_hostname",
      "sequence",
      "cookies",
    ]);
    expect(SyncEnvelope.properties?.cookies.items?.$ref).toBe("#/components/schemas/Cookie");
    expect(Object.keys(PairRequest.properties ?? {})).toEqual([
      "protocol_version",
      "code",
      "sink_public_key",
      "sink_hostname",
    ]);
    expect(Object.keys(PairResponse.properties ?? {})).toEqual([
      "protocol_version",
      "source_public_key",
      "source_hostname",
      "fingerprint",
    ]);
    expect(PairRequest.properties?.protocol_version.const).toBe(1);
  });

  it("resolves every $ref to a component schema", () => {
    const refs: string[] = [];
    walkSchemas(OPENAPI.paths, (schema) => {
      if (schema.$ref) refs.push(schema.$ref);
    });
    for (const [, schema] of Object.entries(OPENAPI.components.schemas)) {
      for (const property of Object.values(schema.properties ?? {})) {
        if (property.$ref) refs.push(property.$ref);
        if (property.items?.$ref) refs.push(property.items.$ref);
      }
    }
    expect(refs.length).toBeGreaterThan(0);
    for (const ref of refs) {
      const name = ref.replace("#/components/schemas/", "");
      expect(OPENAPI.components.schemas[name], ref).toBeDefined();
    }
  });

  it("keeps every absolute URL on the production origin or the repository, with no email", () => {
    const json = JSON.stringify(OPENAPI);
    // Prose URLs end sentences, so strip trailing punctuation.
    const urls = (json.match(/https?:\/\/[^\s"'<>)]+/g) ?? []).map((url) =>
      url.replace(/[.,;:]+$/, ""),
    );
    expect(urls.length).toBeGreaterThan(0);
    for (const url of urls) {
      expect(
        url === ORIGIN ||
          url.startsWith(`${ORIGIN}/`) ||
          url === GITHUB ||
          url.startsWith(`${GITHUB}/`) ||
          url.startsWith("http://{host}") ||
          url.startsWith("http://my-sink.tailnet.ts.net"),
        url,
      ).toBe(true);
    }
    expect(json).not.toMatch(/[a-z0-9._-]+@[a-z0-9-]+\.[a-z]{2,}/i);
    // No em dash or en dash anywhere in the document (site style rule).
    expect(json).not.toContain(String.fromCharCode(0x2014));
    expect(json).not.toContain(String.fromCharCode(0x2013));
  });
});
