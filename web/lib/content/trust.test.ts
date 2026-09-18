// Trust copy contract (R10, R13). Each of about, contact, privacy
// carries well over 500 characters of factual prose; contact names
// GitHub issues and x.com/mvanhorn and no email; privacy states what
// Vercel request logs hold.

import { describe, it, expect } from "vitest";
import { TRUST_PAGES, trustBodyText, ABOUT, CONTACT, PRIVACY } from "./trust";
import { DEVELOPERS } from "./developers";
import { LINKS } from "./links";

describe("trust copy", () => {
  it("exposes about, contact, privacy, and developers", () => {
    expect(Object.keys(TRUST_PAGES)).toEqual(["about", "contact", "privacy", "developers"]);
    expect(TRUST_PAGES.about).toBe(ABOUT);
    expect(TRUST_PAGES.contact).toBe(CONTACT);
    expect(TRUST_PAGES.privacy).toBe(PRIVACY);
    expect(TRUST_PAGES.developers).toBe(DEVELOPERS);
  });

  it("every entry has more than 500 characters of body text", () => {
    for (const page of Object.values(TRUST_PAGES)) {
      expect(trustBodyText(page).length).toBeGreaterThan(500);
      expect(page.title.length).toBeGreaterThan(0);
      expect(page.sections.length).toBeGreaterThan(0);
      for (const section of page.sections) {
        expect(section.heading.length).toBeGreaterThan(0);
        expect(section.paragraphs.length).toBeGreaterThan(0);
      }
    }
  });

  it("no entry publishes an email address or a test count", () => {
    for (const page of Object.values(TRUST_PAGES)) {
      const text = trustBodyText(page);
      expect(text).not.toMatch(/[a-z0-9._-]+@[a-z0-9-]+\.[a-z]{2,}/i);
      expect(text).not.toMatch(/\d+\+? (unit )?tests/i);
    }
  });

  it("contact names GitHub issues and x.com/mvanhorn, and says no email is published", () => {
    const text = trustBodyText(CONTACT);
    expect(text).toContain("https://github.com/mvanhorn/agentcookie/issues");
    expect(text).toContain("x.com/mvanhorn");
    expect(text).toMatch(/no email address/i);
    expect(text).toMatch(/agentcookie doctor/);
    expect(CONTACT.links.map((l) => l.href)).toContain(
      "https://github.com/mvanhorn/agentcookie/issues",
    );
    expect(CONTACT.links.map((l) => l.href)).toContain("https://x.com/mvanhorn");
  });

  it("privacy states no cookies, no analytics, self-hosted fonts, and Vercel request logs with the query string", () => {
    const text = trustBodyText(PRIVACY);
    expect(text).toMatch(/request logs/i);
    expect(text).toMatch(/query string/i);
    expect(text).toMatch(/Vercel/);
    expect(text).toMatch(/no cookies|sets no cookies/i);
    expect(text).toMatch(/analytics/i);
    expect(text).toMatch(/fonts/i);
    expect(text).toMatch(/Tailscale/);
    expect(text).toMatch(/telemetry/i);
    expect(PRIVACY.links.map((l) => l.href)).toContain(LINKS.threatModel);
  });

  it("about names the maintainer, the MIT license, and links the repo and docs", () => {
    const text = trustBodyText(ABOUT);
    expect(text).toContain("Matt Van Horn");
    expect(text).toContain("MIT");
    expect(text).toContain("Tailscale");
    expect(text).toContain("live CDP injection");
    const hrefs = ABOUT.links.map((l) => l.href);
    expect(hrefs).toContain(LINKS.github);
    expect(hrefs).toContain(LINKS.quickstart);
    expect(hrefs).toContain(LINKS.v2AdoptionSpec);
    expect(hrefs).toContain(LINKS.threatModel);
  });
});
