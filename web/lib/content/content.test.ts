// Content module <-> homepage contract.
//
// The Markdown twins render from lib/content/*. This test proves the
// HTML homepage renders from the same modules: every feature title,
// FAQ question, terminal command, and link URL in the modules must
// appear in the rendered page, and the README facts the modules carry
// (Linux sink via live CDP injection, no "macOS only on both ends")
// must be what the page says.

// @vitest-environment jsdom

import React from "react";
import { describe, it, expect, afterEach, vi } from "vitest";
import { render, cleanup } from "@testing-library/react";
import MarketingHome from "@/app/(marketing)/page";
import { FEATURES } from "./features";
import { FAQS } from "./faq";
import { LINKS } from "./links";
import { renderTwin } from "@/lib/markdown";
import {
  HERO,
  TERMINAL_COMMANDS,
  FEATURE_GRID,
  FOOTER_LINE,
  SITE_TITLE,
  SITE_DESCRIPTION,
} from "./home";
import { SITE_ORIGIN } from "@/lib/site";

vi.mock("server-only", () => ({}));

function renderHome() {
  render(MarketingHome() as React.ReactElement);
  return document.body.textContent ?? "";
}

describe("content modules feed the homepage", () => {
  afterEach(() => {
    cleanup();
  });

  it("renders the hero tagline verbatim", () => {
    const text = renderHome();
    expect(text).toContain(HERO.tagline);
  });

  it("renders every feature title", () => {
    const text = renderHome();
    for (const feature of FEATURES) {
      expect(text).toContain(feature.title);
    }
  });

  it("renders every FAQ question and paragraph", () => {
    const text = renderHome();
    for (const faq of FAQS) {
      expect(text).toContain(faq.question);
      for (const para of faq.answer) {
        expect(text).toContain(para);
      }
    }
  });

  it("renders every terminal command", () => {
    const text = renderHome();
    expect(TERMINAL_COMMANDS.length).toBeGreaterThan(0);
    for (const command of TERMINAL_COMMANDS) {
      expect(text).toContain(command);
    }
  });

  it("links to every URL in the links module", () => {
    renderHome();
    const hrefs = Array.from(
      document.querySelectorAll<HTMLAnchorElement>("a"),
    ).map((a) => a.href);
    for (const url of Object.values(LINKS)) {
      expect(hrefs).toContain(url);
    }
  });

  it("states README facts: Linux sink via live CDP injection, no macOS-only claim", () => {
    const text = renderHome();
    expect(text).not.toContain("macOS only on both ends");
    expect(text).not.toContain("macOS only.");
    expect(text).toContain(FEATURE_GRID.trailer);
    expect(text).toContain("live CDP injection");
    expect(text).toContain("Linux");
    expect(text).toContain("520+ unit tests across 26 packages");
    expect(text).toContain(FOOTER_LINE);
  });

  it("site title and description name the Linux box, not only a second Mac", () => {
    expect(SITE_TITLE).toContain("Linux box");
    expect(SITE_DESCRIPTION).toContain("Linux box");
    expect(SITE_DESCRIPTION).toContain("Tailscale");
  });

  it("has no stale second-Mac terminal framing in the page or the /md twin", () => {
    const text = renderHome();
    expect(text).not.toContain("second-Mac");
    expect(text).not.toContain("second-mac");
    const twin = renderTwin("home");
    expect(twin).not.toContain("second-Mac");
    expect(twin).not.toContain("second-mac");
  });

  it("does not mention an email address anywhere", () => {
    const text = renderHome();
    expect(text).not.toMatch(/@[a-z0-9-]+\.[a-z]{2,}/i);
    expect(text).not.toMatch(/\bemail\b/i);
  });

  it("site origin is the literal production host", () => {
    expect(SITE_ORIGIN).toBe("https://agentcookie.dev");
  });
});
