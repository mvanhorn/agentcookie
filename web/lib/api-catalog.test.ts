// /.well-known/api-catalog contract (RFC 9727): a linkset+json body
// with the RFC 9727 profile whose one member anchors on the OpenAPI
// document URL and links service-desc to it and service-doc to
// /developers, served with the same CORS, ETag, cache, preflight, and
// 405 behaviour as /openapi.json.

import { describe, it, expect } from "vitest";
import {
  API_CATALOG,
  API_CATALOG_JSON,
  API_CATALOG_CONTENT_TYPE,
  GET,
  HEAD,
  OPTIONS,
  POST,
  PUT,
  PATCH,
  DELETE,
  etag,
} from "./api-catalog";
import { weakEtag } from "./static-document";

const ORIGIN = "https://agentcookie.dev";
const URL_ = "http://localhost:3000/.well-known/api-catalog";

function req(init: RequestInit = {}): Request {
  return new Request(URL_, init);
}

describe("API catalog linkset", () => {
  it("has one member anchored on the OpenAPI document with service-desc and service-doc", () => {
    expect(API_CATALOG.linkset).toHaveLength(1);
    const member = API_CATALOG.linkset[0];
    expect(member.anchor).toBe(`${ORIGIN}/openapi.json`);
    expect(member["service-desc"]).toEqual([
      expect.objectContaining({
        href: `${ORIGIN}/openapi.json`,
        type: "application/vnd.oai.openapi+json",
      }),
    ]);
    expect(member["service-doc"]).toEqual([
      expect.objectContaining({ href: `${ORIGIN}/developers`, type: "text/html" }),
    ]);
  });

  it("keeps every URL on the production origin", () => {
    const urls = API_CATALOG_JSON.match(/https?:\/\/[^\s"]+/g) ?? [];
    expect(urls.length).toBeGreaterThan(0);
    for (const url of urls) expect(url.startsWith(`${ORIGIN}/`), url).toBe(true);
  });
});

describe("GET /.well-known/api-catalog", () => {
  it("serves linkset+json with the RFC 9727 profile, CORS, nosniff, cache, and ETag", async () => {
    const response = GET(req());
    expect(response.status).toBe(200);
    expect(response.headers.get("content-type")).toBe(
      'application/linkset+json; profile="https://www.rfc-editor.org/info/rfc9727"',
    );
    expect(response.headers.get("content-type")).toBe(API_CATALOG_CONTENT_TYPE);
    expect(response.headers.get("access-control-allow-origin")).toBe("*");
    expect(response.headers.get("x-content-type-options")).toBe("nosniff");
    expect(response.headers.get("cache-control")).toBe("public, s-maxage=3600");
    expect(response.headers.get("etag")).toBe(etag);
    expect(etag).toBe(weakEtag(API_CATALOG_JSON));
    const body = await response.text();
    expect(body).toBe(API_CATALOG_JSON);
    expect(JSON.parse(body)).toEqual(API_CATALOG);
  });

  it("answers 304 to a matching If-None-Match and 200 otherwise", async () => {
    const hit = GET(req({ headers: { "if-none-match": etag } }));
    expect(hit.status).toBe(304);
    expect(await hit.text()).toBe("");
    expect(hit.headers.get("etag")).toBe(etag);
    expect(GET(req({ headers: { "if-none-match": 'W/"stale"' } })).status).toBe(200);
  });

  it("HEAD matches GET headers with an empty body", async () => {
    const get = GET(req());
    const head = HEAD(req({ method: "HEAD" }));
    expect(head.status).toBe(200);
    expect(await head.text()).toBe("");
    for (const [key, value] of get.headers) expect(head.headers.get(key), key).toBe(value);
  });

  it("OPTIONS is a 204 preflight", async () => {
    const response = OPTIONS(req({ method: "OPTIONS" }));
    expect(response.status).toBe(204);
    expect(await response.text()).toBe("");
    expect(response.headers.get("access-control-allow-origin")).toBe("*");
    expect(response.headers.get("access-control-allow-methods")).toBe("GET, HEAD, OPTIONS");
  });

  it("writing methods answer an RFC 9457 405 naming this path", async () => {
    const handlers = { POST, PUT, PATCH, DELETE } as const;
    for (const [method, handler] of Object.entries(handlers)) {
      const response = handler(req({ method }));
      expect(response.status, method).toBe(405);
      expect(response.headers.get("content-type")).toBe(
        "application/problem+json; charset=utf-8",
      );
      expect(response.headers.get("allow")).toBe("GET, HEAD, OPTIONS");
      expect(response.headers.get("access-control-allow-origin")).toBe("*");
      const problem = await response.json();
      expect(problem.status).toBe(405);
      expect(problem.type).toBe(`${ORIGIN}/developers#error-method-not-allowed`);
      expect(problem.instance).toBe("/.well-known/api-catalog");
    }
  });
});
