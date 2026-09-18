// JSON-LD identity graph (R8, KTD6): a trusted constant, serialized
// with `<` escaped so it can never close the script tag it lives in.

import { describe, it, expect } from "vitest";
import { GRAPH, serializeJsonLd } from "./jsonld";
import { ISSUES, GITHUB, MAINTAINER_X } from "@/lib/content/links";

const ORIGIN = "https://agentcookie.dev";

type Node = Record<string, unknown>;

function nodes(): Node[] {
  return GRAPH["@graph"] as unknown as Node[];
}

function byType(type: string): Node {
  const node = nodes().find((n) => n["@type"] === type);
  expect(node, `missing ${type} node`).toBeDefined();
  return node as Node;
}

function walk(value: unknown, visit: (key: string, v: unknown) => void) {
  if (Array.isArray(value)) {
    for (const item of value) walk(item, visit);
  } else if (value && typeof value === "object") {
    for (const [k, v] of Object.entries(value as Node)) {
      visit(k, v);
      walk(v, visit);
    }
  }
}

describe("serializeJsonLd", () => {
  it("escapes every < as \\u003c and parses back to the input", () => {
    const sample = { text: "</script><b>", nested: ["<", "a<b"] };
    const out = serializeJsonLd(sample);
    expect(out).not.toContain("<");
    expect(out).toContain("\\u003c/script>");
    expect(JSON.parse(out)).toEqual(sample);
  });

  it("serializes the graph without a raw < and round-trips it", () => {
    const out = serializeJsonLd(GRAPH);
    expect(out).not.toContain("<");
    expect(JSON.parse(out)).toEqual(GRAPH);
  });
});

describe("GRAPH", () => {
  it("is a schema.org @graph with the three identity nodes", () => {
    expect(GRAPH["@context"]).toBe("https://schema.org");
    expect(nodes().map((n) => n["@type"]).sort()).toEqual(
      ["Organization", "SoftwareApplication", "WebSite"].sort()
    );
  });

  it("Organization carries sameAs and a technical-support contactPoint on the issues page", () => {
    const org = byType("Organization");
    expect(org["@id"]).toBe(`${ORIGIN}/#organization`);
    expect(org.name).toBe("agentcookie");
    expect(org.url).toBe(`${ORIGIN}/`);
    expect(org.sameAs).toEqual([GITHUB, MAINTAINER_X]);
    expect(org.contactPoint).toEqual([
      {
        "@type": "ContactPoint",
        contactType: "technical support",
        url: ISSUES,
        availableLanguage: ["English"],
      },
    ]);
  });

  it("SoftwareApplication states category, platforms, free offer, MIT license, and author by @id", () => {
    const app = byType("SoftwareApplication");
    expect(app["@id"]).toBe(`${ORIGIN}/#software`);
    expect(app.applicationCategory).toBe("DeveloperApplication");
    expect(app.operatingSystem).toBe("macOS (source); Linux or macOS (sink)");
    expect(app.offers).toMatchObject({ price: "0", priceCurrency: "USD" });
    expect(app.license).toBe("https://spdx.org/licenses/MIT.html");
    expect(app.author).toEqual({ "@id": `${ORIGIN}/#organization` });
    expect(app.downloadUrl).toBe(`${GITHUB}/releases`);
  });

  it("WebSite names its publisher and subject by @id", () => {
    const site = byType("WebSite");
    expect(site["@id"]).toBe(`${ORIGIN}/#website`);
    expect(site.url).toBe(`${ORIGIN}/`);
    expect(site.publisher).toEqual({ "@id": `${ORIGIN}/#organization` });
    expect(site.about).toEqual({ "@id": `${ORIGIN}/#software` });
  });

  it("has no email or address anywhere", () => {
    walk(GRAPH, (key) => {
      expect(key).not.toBe("email");
      expect(key).not.toBe("address");
    });
  });

  it("builds every url and @id from the production origin, except the intended external links", () => {
    const external = new Set<string>([GITHUB, MAINTAINER_X, ISSUES, `${GITHUB}/releases`]);
    walk(GRAPH, (key, value) => {
      if (key !== "url" && key !== "@id") return;
      if (typeof value !== "string") return;
      if (external.has(value)) return;
      expect(value.startsWith(ORIGIN), `${key}=${value}`).toBe(true);
    });
  });
});
