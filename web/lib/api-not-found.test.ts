// /api catch-all contract: every method on every path answers an RFC
// 9457 problem with status 404 that reflects the pathname only, plus
// a 204 preflight for OPTIONS.

import { describe, it, expect } from "vitest";
import {
  handleApiRequest,
  API_NOT_FOUND_DETAIL,
  GET,
  HEAD,
  POST,
  PUT,
  PATCH,
  DELETE,
  OPTIONS,
} from "./api-not-found";

const BASE = "http://localhost:3000";
const ORIGIN = "https://agentcookie.dev";

function api(path: string, init: RequestInit = {}): Response {
  return handleApiRequest(new Request(`${BASE}${path}`, init));
}

const EXPECTED_HEADERS = {
  "content-type": "application/problem+json; charset=utf-8",
  "cache-control": "no-store",
  "access-control-allow-origin": "*",
  "x-content-type-options": "nosniff",
  "x-robots-tag": "noindex",
} as const;

describe("GET /api/*", () => {
  it("answers a 404 problem that points at /openapi.json and /developers", async () => {
    const response = api("/api/v1/sync");
    expect(response.status).toBe(404);
    for (const [key, value] of Object.entries(EXPECTED_HEADERS)) {
      expect(response.headers.get(key), key).toBe(value);
    }
    expect(await response.json()).toEqual({
      type: `${ORIGIN}/developers#error-not-found`,
      title: "Not Found",
      status: 404,
      detail: API_NOT_FOUND_DETAIL,
      instance: "/api/v1/sync",
      links: {
        openapi: `${ORIGIN}/openapi.json`,
        developers: `${ORIGIN}/developers`,
      },
    });
    expect(API_NOT_FOUND_DETAIL).toBe(
      "agentcookie.dev hosts no API; the sink HTTP interface runs on your tailnet",
    );
  });

  it("covers the bare /api path", async () => {
    const response = api("/api");
    expect(response.status).toBe(404);
    expect((await response.json()).instance).toBe("/api");
  });

  it("never reflects the query string, headers, or body", async () => {
    const response = api("/api/x?secret=LEAK&q=%3Cscript%3E", {
      method: "POST",
      headers: {
        authorization: "Bearer TOKENVALUE",
        host: "evil.example",
        origin: "https://evil.example",
        "content-type": "application/json",
      },
      body: JSON.stringify({ injected: "BODYVALUE" }),
    });
    expect(response.status).toBe(404);
    const text = await response.text();
    expect(text).not.toContain("LEAK");
    expect(text).not.toContain("secret");
    expect(text).not.toContain("script");
    expect(text).not.toContain("TOKENVALUE");
    expect(text).not.toContain("BODYVALUE");
    expect(text).not.toContain("evil.example");
    expect(JSON.parse(text).instance).toBe("/api/x");
    for (const [, value] of response.headers) {
      expect(value).not.toContain("evil.example");
    }
  });
});

describe("other methods on /api/*", () => {
  it("GET, HEAD, POST, PUT, PATCH, and DELETE all answer the same 404 problem", async () => {
    const handlers = { GET, HEAD, POST, PUT, PATCH, DELETE } as const;
    for (const [method, handler] of Object.entries(handlers)) {
      const response = handler(new Request(`${BASE}/api/thing`, { method }));
      expect(response.status, method).toBe(404);
      expect(response.headers.get("content-type"), method).toBe(EXPECTED_HEADERS["content-type"]);
      expect(response.headers.get("access-control-allow-origin"), method).toBe("*");
      const text = await response.text();
      if (method === "HEAD") {
        expect(text).toBe("");
      } else {
        expect(JSON.parse(text).status).toBe(404);
      }
    }
  });

  it("OPTIONS is a 204 preflight with CORS headers and no body", async () => {
    const response = OPTIONS(new Request(`${BASE}/api/thing`, { method: "OPTIONS" }));
    expect(response.status).toBe(204);
    expect(await response.text()).toBe("");
    expect(response.headers.get("access-control-allow-origin")).toBe("*");
    expect(response.headers.get("access-control-allow-methods")).toContain("GET");
    expect(response.headers.get("access-control-allow-methods")).toContain("POST");
    expect(response.headers.get("allow")).toContain("OPTIONS");
  });
});
