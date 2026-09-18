// Root layout metadata contract: metadataBase is the literal
// production origin, independent of any deployment environment
// variable.

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

// next/font/google is an empty module outside Next's font loader.
vi.mock("next/font/google", () => ({
  Geist: () => ({ variable: "" }),
  Geist_Mono: () => ({ variable: "" }),
}));
vi.mock("./globals.css", () => ({}));

const ORIGINAL = process.env.NEXT_PUBLIC_PLATFORM_URL;

describe("root layout metadata", () => {
  beforeEach(() => {
    vi.resetModules();
  });
  afterEach(() => {
    if (ORIGINAL === undefined) {
      delete process.env.NEXT_PUBLIC_PLATFORM_URL;
    } else {
      process.env.NEXT_PUBLIC_PLATFORM_URL = ORIGINAL;
    }
  });

  it("metadataBase is https://agentcookie.dev regardless of NEXT_PUBLIC_PLATFORM_URL", async () => {
    process.env.NEXT_PUBLIC_PLATFORM_URL = "https://agentcookie.vercel.app";
    const { metadata } = await import("./layout");
    expect(metadata.metadataBase?.href).toBe("https://agentcookie.dev/");
  });

  it("declares the is-agentic site type as content (R9)", async () => {
    const { metadata } = await import("./layout");
    expect(metadata.other?.["is-agentic-site-type"]).toBe("content");
  });
});
