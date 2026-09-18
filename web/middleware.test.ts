// Middleware contract (KTD1): rewrite Markdown-preferring GET/HEAD page
// requests to /md/<path>; otherwise pass through with Vary: Accept and
// an alternate Link built from SITE_ORIGIN. The matcher keeps assets,
// the md tree, and files out of the middleware entirely.

import { describe, it, expect } from "vitest";
import { NextRequest } from "next/server";
import {
  unstable_doesMiddlewareMatch,
  isRewrite,
  getRewrittenUrl,
} from "next/experimental/testing/server";
import { middleware, config, mergeVary } from "./middleware";

const BASE = "http://localhost:3000";

function request(
  path: string,
  headers: Record<string, string> = {},
  method = "GET",
): NextRequest {
  return new NextRequest(`${BASE}${path}`, { headers, method });
}

function matches(path: string, headers?: Record<string, string>): boolean {
  return unstable_doesMiddlewareMatch({
    config,
    url: `${BASE}${path}`,
    headers,
  });
}

describe("middleware matcher", () => {
  it("skips assets, files, the og image, and the md tree", () => {
    for (const path of [
      "/opengraph-image",
      "/_next/static/x.js",
      "/_next/image?url=x",
      "/_vercel/insights/script.js",
      "/sitemap.xml",
      "/llms.txt",
      "/robots.txt",
      "/favicon.ico",
      "/md",
      "/md/about",
      "/md/md/about",
      "/api",
      "/api/",
      "/api/v1/sync",
      "/.well-known",
      "/.well-known/api-catalog",
      "/openapi.json",
    ]) {
      expect(matches(path), path).toBe(false);
    }
  });

  it("covers pages and unknown paths", () => {
    for (const path of [
      "/",
      "/about",
      "/contact",
      "/privacy",
      "/developers",
      "/nope",
      "/mdx",
      "/apix",
      "/well-known",
    ]) {
      expect(matches(path), path).toBe(true);
    }
  });

  it("does not match /md even with a Markdown preference", () => {
    expect(matches("/md", { accept: "text/markdown" })).toBe(false);
  });

  it("never rewrites the api tree or the well-known tree to a Markdown twin", () => {
    for (const path of ["/api/x", "/api", "/.well-known/api-catalog"]) {
      expect(matches(path, { accept: "text/markdown" }), path).toBe(false);
    }
  });
});

describe("middleware rewrite", () => {
  it("rewrites /about to /md/about on a Markdown preference", () => {
    const response = middleware(request("/about", { accept: "text/markdown" }));
    expect(isRewrite(response)).toBe(true);
    expect(getRewrittenUrl(response)).toBe(`${BASE}/md/about`);
  });

  it("rewrites / to /md and preserves the query string", () => {
    const response = middleware(
      request("/?utm=1", { accept: "text/markdown, text/html" }),
    );
    expect(isRewrite(response)).toBe(true);
    expect(getRewrittenUrl(response)).toBe(`${BASE}/md?utm=1`);
  });

  it("rewrites HEAD but not POST", () => {
    const head = middleware(request("/about", { accept: "text/markdown" }, "HEAD"));
    expect(isRewrite(head)).toBe(true);
    const post = middleware(request("/about", { accept: "text/markdown" }, "POST"));
    expect(isRewrite(post)).toBe(false);
  });
});

describe("middleware pass-through", () => {
  it("adds Vary: Accept once and an alternate Link for /about", () => {
    const response = middleware(request("/about", { accept: "text/html" }));
    expect(isRewrite(response)).toBe(false);
    const vary = (response.headers.get("vary") ?? "")
      .split(",")
      .map((v) => v.trim().toLowerCase());
    expect(vary.filter((v) => v === "accept")).toHaveLength(1);
    expect(response.headers.get("link")).toBe(
      '<https://agentcookie.dev/md/about>; rel="alternate"; type="text/markdown"',
    );
  });

  it("points the alternate Link for / at /md and strips the search", () => {
    const response = middleware(request("/?x=1", { accept: "*/*" }));
    expect(isRewrite(response)).toBe(false);
    expect(response.headers.get("link")).toBe(
      '<https://agentcookie.dev/md>; rel="alternate"; type="text/markdown"',
    );
  });

  it("passes through with no Accept header", () => {
    const response = middleware(request("/nope"));
    expect(isRewrite(response)).toBe(false);
    expect(response.headers.get("link")).toContain("https://agentcookie.dev/md/nope");
  });

  it("never reflects Host or Origin", () => {
    const hostile = {
      accept: "text/html",
      host: "evil.example",
      origin: "https://evil.example",
      "x-forwarded-host": "evil.example",
    };
    const response = middleware(request("/about", hostile));
    for (const [, value] of response.headers) {
      expect(value).not.toContain("evil.example");
    }
    expect(response.headers.get("link")).toContain("https://agentcookie.dev/md/about");
    // An Origin header changes nothing about the decision either.
    const plain = middleware(request("/about", { accept: "text/html" }));
    expect(response.headers.get("link")).toBe(plain.headers.get("link"));
    expect(isRewrite(response)).toBe(isRewrite(plain));
    const rewritten = middleware(request("/about", { ...hostile, accept: "text/markdown" }));
    expect(getRewrittenUrl(rewritten)).toBe(`${BASE}/md/about`);
  });
});

describe("mergeVary", () => {
  it("appends Accept without duplicating it", () => {
    expect(mergeVary(null)).toBe("Accept");
    expect(mergeVary("")).toBe("Accept");
    expect(mergeVary("RSC, Next-Router-State-Tree")).toBe(
      "RSC, Next-Router-State-Tree, Accept",
    );
    expect(mergeVary("accept")).toBe("accept");
    expect(mergeVary("RSC, Accept , RSC")).toBe("RSC, Accept");
  });
});
