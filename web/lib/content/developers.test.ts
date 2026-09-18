// Developers copy contract. The page documents the sink interface
// that runs on the user's tailnet, says there is no hosted API, names
// the three endpoints and the pairing handshake, and carries the two
// error sections the problem+json `type` URIs point at. Facts here
// must match lib/openapi.ts and the /api handler.

import { describe, it, expect } from "vitest";
import { DEVELOPERS, OPENAPI_URL, API_CATALOG_URL } from "./developers";
import { trustBodyText } from "./trust";
import { LINKS, RELEASES } from "./links";
import { OPENAPI } from "@/lib/openapi";
import { API_NOT_FOUND_DETAIL } from "@/lib/api-not-found";

describe("developers copy", () => {
  const text = trustBodyText(DEVELOPERS);

  it("has the developers key and more than 500 characters of factual prose", () => {
    expect(DEVELOPERS.key).toBe("developers");
    expect(DEVELOPERS.title).toBe("Developers");
    expect(text.length).toBeGreaterThan(500);
    expect(text).not.toContain("!");
    expect(DEVELOPERS.description).not.toContain("!");
  });

  it("says where the interface runs and that nothing is hosted here", () => {
    expect(text).toMatch(/tailnet/);
    expect(text).toContain("9999");
    expect(text).toContain("9998");
    expect(text).toContain("my-sink.tailnet.ts.net");
    expect(text).toMatch(/no API key/);
    expect(text).toMatch(/no sign-up/);
    expect(text).toMatch(/nothing to call/);
    expect(text).toContain(API_NOT_FOUND_DETAIL);
  });

  it("names the three endpoints from the OpenAPI document and the pairing handshake", () => {
    for (const path of Object.keys(OPENAPI.paths)) {
      expect(text).toContain(path);
    }
    expect(text).toContain("GET /healthz");
    expect(text).toContain("POST /sync");
    expect(text).toContain("POST /pair");
    expect(text).toMatch(/X25519/);
    expect(text).toMatch(/HKDF-SHA256/);
    expect(text).toMatch(/AES-256-GCM/);
    expect(text).toMatch(/replay/);
  });

  it("carries a curl example against a tailnet host", () => {
    const code = DEVELOPERS.sections.flatMap((s) => s.code ?? []);
    expect(code).toEqual(["$ curl http://my-sink.tailnet.ts.net:9999/healthz", "ok"]);
  });

  it("has the error-not-found and error-method-not-allowed sections", () => {
    const ids = DEVELOPERS.sections.map((s) => s.id).filter(Boolean);
    expect(ids).toEqual(["error-not-found", "error-method-not-allowed"]);
    const notFound = DEVELOPERS.sections.find((s) => s.id === "error-not-found");
    expect(notFound?.paragraphs.join(" ")).toMatch(/application\/problem\+json/);
    expect(notFound?.paragraphs.join(" ")).toMatch(/RFC 9457/);
    expect(notFound?.paragraphs.join(" ")).toMatch(/without the query string/);
  });

  it("links the OpenAPI document, the catalog, the quickstart, the spec, releases, and the repo", () => {
    const hrefs = DEVELOPERS.links.map((l) => l.href);
    expect(OPENAPI_URL).toBe("https://agentcookie.dev/openapi.json");
    expect(API_CATALOG_URL).toBe("https://agentcookie.dev/.well-known/api-catalog");
    expect(hrefs).toContain(OPENAPI_URL);
    expect(hrefs).toContain(API_CATALOG_URL);
    expect(hrefs).toContain(LINKS.quickstart);
    expect(hrefs).toContain(LINKS.secretsBusV1Spec);
    expect(hrefs).toContain(RELEASES);
    expect(hrefs).toContain(LINKS.github);
    for (const href of hrefs) {
      expect(
        href.startsWith("https://agentcookie.dev/") || href.startsWith("https://github.com/mvanhorn/agentcookie"),
        href,
      ).toBe(true);
    }
  });

  it("publishes no email address, no postal address, and no dashes of the wrong kind", () => {
    const all = JSON.stringify(DEVELOPERS);
    expect(all).not.toMatch(/[a-z0-9._-]+@[a-z0-9-]+\.[a-z]{2,}/i);
    expect(all).not.toMatch(/\bemail\b/i);
    expect(all).not.toContain(String.fromCharCode(0x2014));
    expect(all).not.toContain(String.fromCharCode(0x2013));
  });
});
