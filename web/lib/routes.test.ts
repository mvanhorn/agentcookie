// Static route table contract. The sitemap, footer, llms.txt, and the
// Markdown handler all read this table; every page must carry a
// title and an updatedAt that parses as a date.

import { describe, it, expect } from "vitest";
import { ROUTES, findRoute } from "./routes";

describe("static route table", () => {
  it("lists exactly /, /about, /contact, /privacy, /developers in order", () => {
    expect(ROUTES.map((r) => r.path)).toEqual([
      "/",
      "/about",
      "/contact",
      "/privacy",
      "/developers",
    ]);
  });

  it("every entry has a non-empty title", () => {
    for (const route of ROUTES) {
      expect(route.title.trim().length).toBeGreaterThan(0);
    }
  });

  it("every entry has an ISO date updatedAt that parses", () => {
    for (const route of ROUTES) {
      expect(route.updatedAt).toMatch(/^\d{4}-\d{2}-\d{2}$/);
      expect(Number.isNaN(Date.parse(route.updatedAt))).toBe(false);
    }
  });

  it("every entry names a distinct twin renderer", () => {
    const twins = ROUTES.map((r) => r.twin);
    expect(new Set(twins).size).toBe(ROUTES.length);
  });

  it("findRoute resolves known paths and rejects unknown ones", () => {
    expect(findRoute("/")?.twin).toBe("home");
    expect(findRoute("/about")?.twin).toBe("about");
    expect(findRoute("/developers")?.twin).toBe("developers");
    expect(findRoute("/nope")).toBeUndefined();
    expect(findRoute("/md/about")).toBeUndefined();
  });
});
