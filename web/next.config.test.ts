// next.config.ts headers() (R6): the CDN must key HTML on Accept and
// must not sniff any response into another type.

import { describe, it, expect } from "vitest";
import nextConfig from "./next.config";

describe("next.config headers()", () => {
  it("sets Vary: Accept and X-Content-Type-Options: nosniff on every non-_next path", async () => {
    const headers = await nextConfig.headers!();
    const rule = headers.find((h) => h.source === "/((?!_next/).*)");
    expect(rule).toBeDefined();
    expect(rule?.headers).toContainEqual({ key: "Vary", value: "Accept" });
    expect(rule?.headers).toContainEqual({
      key: "X-Content-Type-Options",
      value: "nosniff",
    });
  });
});
