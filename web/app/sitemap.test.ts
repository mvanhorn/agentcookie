// Crawler files: sitemap and robots both read the literal production
// origin (R11, R15) and the static route table, never metadataBase or
// an environment variable.

import { describe, it, expect } from "vitest";
import sitemap from "./sitemap";
import robots from "./robots";
import { ROUTES } from "@/lib/routes";

const ORIGIN = "https://agentcookie.dev";

describe("sitemap.xml", () => {
  it("lists exactly the five static routes on the production origin", () => {
    const entries = sitemap();
    expect(entries.map((e) => e.url)).toEqual([
      `${ORIGIN}/`,
      `${ORIGIN}/about`,
      `${ORIGIN}/contact`,
      `${ORIGIN}/privacy`,
      `${ORIGIN}/developers`,
    ]);
    expect(entries).toHaveLength(ROUTES.length);
    expect(entries).toHaveLength(5);
  });

  it("gives every entry a lastModified taken from the route table", () => {
    const entries = sitemap();
    for (const route of ROUTES) {
      const entry = entries.find((e) => e.url === `${ORIGIN}${route.path}`);
      expect(entry?.lastModified).toBe(route.updatedAt);
    }
  });

  it("ignores the platform URL variable", () => {
    const before = process.env.NEXT_PUBLIC_PLATFORM_URL;
    process.env.NEXT_PUBLIC_PLATFORM_URL = "https://agentcookie.vercel.app";
    try {
      for (const entry of sitemap()) {
        expect(entry.url.startsWith(ORIGIN)).toBe(true);
      }
    } finally {
      if (before === undefined) delete process.env.NEXT_PUBLIC_PLATFORM_URL;
      else process.env.NEXT_PUBLIC_PLATFORM_URL = before;
    }
  });
});

describe("robots.txt", () => {
  it("allows every agent on / and names the production sitemap", () => {
    const result = robots();
    const rules = Array.isArray(result.rules) ? result.rules : [result.rules];
    expect(rules).toEqual([{ userAgent: "*", allow: "/" }]);
    expect(result.sitemap).toBe(`${ORIGIN}/sitemap.xml`);
  });
});
