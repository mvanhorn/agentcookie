// /openapi.json handler contract: the document as OpenAPI JSON with
// CORS, a weak ETag honoured by If-None-Match, an hour of shared
// cache, a 204 preflight, and an RFC 9457 405 for writing methods.

import { describe, it, expect } from "vitest";
import {
  GET,
  HEAD,
  OPTIONS,
  POST,
  PUT,
  PATCH,
  DELETE,
  OPENAPI_JSON,
  OPENAPI_CONTENT_TYPE,
  etag,
} from "./openapi-route";
import { OPENAPI } from "./openapi";
import { ifNoneMatchMatches, weakEtag } from "./static-document";

const URL_ = "http://localhost:3000/openapi.json";

function req(init: RequestInit = {}): Request {
  return new Request(URL_, init);
}

describe("GET /openapi.json", () => {
  it("serves the document with the OpenAPI media type, CORS, nosniff, cache, and ETag", async () => {
    const response = GET(req());
    expect(response.status).toBe(200);
    expect(response.headers.get("content-type")).toBe(
      "application/vnd.oai.openapi+json; charset=utf-8",
    );
    expect(response.headers.get("content-type")).toBe(OPENAPI_CONTENT_TYPE);
    expect(response.headers.get("access-control-allow-origin")).toBe("*");
    expect(response.headers.get("x-content-type-options")).toBe("nosniff");
    expect(response.headers.get("cache-control")).toBe("public, s-maxage=3600");
    expect(response.headers.get("etag")).toBe(etag);
    expect(etag).toMatch(/^W\/"[0-9a-f]{32}"$/);
    const body = await response.text();
    expect(body).toBe(OPENAPI_JSON);
    expect(JSON.parse(body)).toEqual(OPENAPI);
  });

  it("computes the ETag from the body once", () => {
    expect(etag).toBe(weakEtag(OPENAPI_JSON));
    expect(GET(req()).headers.get("etag")).toBe(GET(req()).headers.get("etag"));
  });

  it("answers 304 with no body to a matching If-None-Match", async () => {
    for (const value of [etag, etag.replace(/^W\//, ""), `"other", ${etag}`, "*"]) {
      const response = GET(req({ headers: { "if-none-match": value } }));
      expect(response.status, value).toBe(304);
      expect(await response.text()).toBe("");
      expect(response.headers.get("etag")).toBe(etag);
      expect(response.headers.get("cache-control")).toBe("public, s-maxage=3600");
      expect(response.headers.get("access-control-allow-origin")).toBe("*");
      expect(response.headers.get("content-type")).toBeNull();
    }
  });

  it("serves 200 to a non-matching If-None-Match", () => {
    const response = GET(req({ headers: { "if-none-match": 'W/"nope"' } }));
    expect(response.status).toBe(200);
  });
});

describe("HEAD /openapi.json", () => {
  it("returns the GET headers with an empty body", async () => {
    const get = GET(req());
    const head = HEAD(req({ method: "HEAD" }));
    expect(head.status).toBe(200);
    expect(await head.text()).toBe("");
    for (const [key, value] of get.headers) {
      expect(head.headers.get(key), key).toBe(value);
    }
  });
});

describe("OPTIONS /openapi.json", () => {
  it("is a 204 preflight with CORS headers", async () => {
    const response = OPTIONS(req({ method: "OPTIONS" }));
    expect(response.status).toBe(204);
    expect(await response.text()).toBe("");
    expect(response.headers.get("access-control-allow-origin")).toBe("*");
    expect(response.headers.get("access-control-allow-methods")).toBe("GET, HEAD, OPTIONS");
    expect(response.headers.get("allow")).toBe("GET, HEAD, OPTIONS");
    expect(response.headers.get("access-control-allow-headers")).toContain("If-None-Match");
  });
});

describe("writing methods on /openapi.json", () => {
  it("answer an RFC 9457 405 with Allow and CORS", async () => {
    const handlers = { POST, PUT, PATCH, DELETE } as const;
    for (const [method, handler] of Object.entries(handlers)) {
      const response = handler(req({ method, body: method === "DELETE" ? null : "{}" }));
      expect(response.status, method).toBe(405);
      expect(response.headers.get("content-type")).toBe(
        "application/problem+json; charset=utf-8",
      );
      expect(response.headers.get("allow")).toBe("GET, HEAD, OPTIONS");
      expect(response.headers.get("access-control-allow-origin")).toBe("*");
      expect(response.headers.get("cache-control")).toBe("no-store");
      expect(response.headers.get("x-content-type-options")).toBe("nosniff");
      const problem = await response.json();
      expect(problem).toEqual({
        type: "https://agentcookie.dev/developers#error-method-not-allowed",
        title: "Method Not Allowed",
        status: 405,
        detail: "This document is read-only; use GET, HEAD, OPTIONS.",
        instance: "/openapi.json",
        links: {
          openapi: "https://agentcookie.dev/openapi.json",
          developers: "https://agentcookie.dev/developers",
        },
      });
    }
  });
});

describe("ifNoneMatchMatches", () => {
  it("compares weakly across lists and the wildcard", () => {
    const tag = 'W/"abc"';
    expect(ifNoneMatchMatches(null, tag)).toBe(false);
    expect(ifNoneMatchMatches("", tag)).toBe(false);
    expect(ifNoneMatchMatches('"abc"', tag)).toBe(true);
    expect(ifNoneMatchMatches('W/"abc"', tag)).toBe(true);
    expect(ifNoneMatchMatches('"x", W/"abc"', tag)).toBe(true);
    expect(ifNoneMatchMatches("*", tag)).toBe(true);
    expect(ifNoneMatchMatches('"abcd"', tag)).toBe(false);
  });
});
