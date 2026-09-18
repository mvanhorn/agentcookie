// Accept negotiation contract (R3). Markdown wins only when an explicit
// text/markdown range with q > 0 beats the effective q of text/html
// (explicit text/html, then text/*, then */*); ties go to the earlier
// listing. Anything missing, malformed, or wildcard-only is HTML.

import { describe, it, expect } from "vitest";
import { preferredRepresentation } from "./negotiate";

const CHROME =
  "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7";

describe("preferredRepresentation", () => {
  it("resolves wildcard, browser, missing, and malformed headers to HTML", () => {
    expect(preferredRepresentation("*/*")).toBe("html");
    expect(preferredRepresentation("text/*")).toBe("html");
    expect(preferredRepresentation(CHROME)).toBe("html");
    expect(preferredRepresentation(null)).toBe("html");
    expect(preferredRepresentation("")).toBe("html");
    expect(preferredRepresentation(";;;,,,=")).toBe("html");
    expect(preferredRepresentation("markdown")).toBe("html");
  });

  it("resolves an explicit Markdown preference to Markdown", () => {
    expect(preferredRepresentation("text/markdown")).toBe("markdown");
    expect(preferredRepresentation("text/markdown, text/html")).toBe(
      "markdown",
    );
    expect(preferredRepresentation("text/html;q=0, text/markdown")).toBe(
      "markdown",
    );
    expect(preferredRepresentation("TEXT/MARKDOWN")).toBe("markdown");
    expect(preferredRepresentation("text/markdown;q=0.9, */*;q=0.8")).toBe(
      "markdown",
    );
    expect(preferredRepresentation("text/markdown;q=1.000")).toBe("markdown");
  });

  it("keeps HTML when its effective q is higher or listed first", () => {
    expect(preferredRepresentation("text/html, text/markdown")).toBe("html");
    expect(preferredRepresentation("text/markdown;q=0.5, */*;q=0.9")).toBe(
      "html",
    );
    expect(preferredRepresentation("text/markdown;q=0.5, text/*;q=0.6")).toBe(
      "html",
    );
    expect(preferredRepresentation("text/*;q=0.5, text/markdown;q=0.5")).toBe(
      "html",
    );
    expect(preferredRepresentation("text/markdown;q=0.5, text/*;q=0.5")).toBe(
      "markdown",
    );
  });

  it("treats a zero or malformed q as an absent range", () => {
    expect(preferredRepresentation("text/markdown;q=0")).toBe("html");
    expect(preferredRepresentation("text/markdown;q=0.000")).toBe("html");
    expect(preferredRepresentation("text/markdown;q=1.5")).toBe("html");
    expect(preferredRepresentation("text/markdown;q=.5")).toBe("html");
    expect(preferredRepresentation("text/markdown;q=0.1234")).toBe("html");
    expect(preferredRepresentation("text/markdown;q=")).toBe("html");
    expect(preferredRepresentation("text/markdown;q=abc")).toBe("html");
    // A malformed q on the html range removes that range, so markdown wins.
    expect(preferredRepresentation("text/html;q=zz, text/markdown")).toBe(
      "markdown",
    );
  });

  it("ignores everything past the 2,048-character and 32-range caps", () => {
    const filler = "application/x-filler;q=0.1, ".repeat(100);
    // text/markdown sits beyond the character cap: never read.
    expect(preferredRepresentation(`${filler}text/markdown`)).toBe("html");
    const manyRanges = Array.from({ length: 40 }, () => "a/b").join(",");
    // text/markdown is the 41st range: never read.
    expect(preferredRepresentation(`${manyRanges},text/markdown`)).toBe(
      "html",
    );
    // The 32nd range is still read.
    const thirtyOne = Array.from({ length: 31 }, () => "a/b").join(",");
    expect(preferredRepresentation(`${thirtyOne},text/markdown`)).toBe(
      "markdown",
    );
  });

  it("resolves a 64KB header and a 10,000-range header within a few ms", () => {
    const huge = "x".repeat(64 * 1024);
    const ranges = Array.from({ length: 10_000 }, () => "text/html").join(",");
    // Warm the JIT once so the timing below measures the parser.
    preferredRepresentation(huge);
    preferredRepresentation(ranges);
    expect(preferredRepresentation(huge)).toBe("html");
    expect(preferredRepresentation(ranges)).toBe("html");
    expect(preferredRepresentation(`${ranges},text/markdown`)).toBe("html");
  });
});
