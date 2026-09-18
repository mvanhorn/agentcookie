// Trust page rendering tests (R7, R10).
//
// /about, /contact, /privacy, and /developers are synchronous Server Components
// that render their lib/content/trust.ts copy inside Shell. Each
// must ship a self-referencing canonical and an Open Graph URL in
// its metadata, and its static render must carry the full prose
// (more than 500 characters), the TopNav logo link, and the footer
// links, so an agent curling the page sees everything at once.
//
// Contact is the product contract check: GitHub issues and
// x.com/mvanhorn are the only channels, and no email ever appears.

// @vitest-environment jsdom

import { OG_IMAGE } from "@/lib/og";
import React from "react";
import { describe, it, expect, afterEach, vi } from "vitest";
import { render, cleanup } from "@testing-library/react";
import AboutPage, { metadata as aboutMetadata } from "./about/page";
import ContactPage, { metadata as contactMetadata } from "./contact/page";
import PrivacyPage, { metadata as privacyMetadata } from "./privacy/page";
import DevelopersPage, { metadata as developersMetadata } from "./developers/page";
import { TRUST_PAGES } from "@/lib/content/trust";
import { API_NOT_FOUND_DETAIL } from "@/lib/api-not-found";
import { FOOTER_LINKS, ISSUES, MAINTAINER_X } from "@/lib/content/links";
import { ROUTES } from "@/lib/routes";

vi.mock("server-only", () => ({}));

const PAGES = [
  { path: "/about", Page: AboutPage, metadata: aboutMetadata, copy: TRUST_PAGES.about },
  { path: "/contact", Page: ContactPage, metadata: contactMetadata, copy: TRUST_PAGES.contact },
  { path: "/privacy", Page: PrivacyPage, metadata: privacyMetadata, copy: TRUST_PAGES.privacy },
  { path: "/developers", Page: DevelopersPage, metadata: developersMetadata, copy: TRUST_PAGES.developers },
] as const;

const TRUST_PATHS = ROUTES.map((route) => route.path).filter(
  (path) => path !== "/",
);

function hrefs(): string[] {
  return Array.from(document.querySelectorAll<HTMLAnchorElement>("a")).map(
    (a) => a.getAttribute("href") ?? "",
  );
}

describe("trust pages", () => {
  afterEach(() => {
    cleanup();
  });

  for (const { path, Page, metadata, copy } of PAGES) {
    describe(path, () => {
      it("exports a self-referencing canonical and an Open Graph URL", () => {
        expect(metadata.alternates?.canonical).toBe(path);
        expect(metadata.openGraph?.url).toBe(path);
        expect(metadata.openGraph?.title).toBe(copy.title);
        expect(metadata.title).toBe(copy.title);
        expect(metadata.description).toBe(copy.description);
        expect(metadata.openGraph?.description).toBe(copy.description);
        expect(metadata.openGraph?.images).toEqual([OG_IMAGE]);
        expect(metadata.twitter?.images).toEqual([OG_IMAGE]);
        expect(metadata.twitter?.title).toBe(copy.title);
        expect(metadata.twitter?.description).toBe(copy.description);
      });

      it("renders more than 500 characters of prose with the title and every section", () => {
        render(Page() as React.ReactElement);
        const text = document.body.textContent ?? "";
        expect(text.length).toBeGreaterThan(500);
        expect(document.querySelector("h1")?.textContent).toBe(copy.title);
        const h2s = Array.from(document.querySelectorAll("h2")).map(
          (h) => h.textContent,
        );
        for (const section of copy.sections) {
          expect(h2s).toContain(section.heading);
          for (const paragraph of section.paragraphs) {
            expect(text).toContain(paragraph);
          }
        }
        for (const link of copy.links) {
          expect(hrefs()).toContain(link.href);
        }
      });

      it("renders inside Shell: nav logo link plus footer links", () => {
        render(Page() as React.ReactElement);
        expect(document.querySelector("[data-marketing-shell]")).not.toBeNull();
        const links = hrefs();
        expect(links).toContain("/");
        for (const link of FOOTER_LINKS) {
          expect(links).toContain(link.href);
        }
        for (const trustPath of TRUST_PATHS) {
          expect(links).toContain(trustPath);
        }
      });
    });
  }

  it("contact links GitHub issues and x.com/mvanhorn and publishes no email address", () => {
    render(ContactPage() as React.ReactElement);
    const text = document.body.textContent ?? "";
    expect(text).toContain(ISSUES);
    expect(text).toContain("x.com/mvanhorn");
    expect(hrefs()).toContain(ISSUES);
    expect(hrefs()).toContain(MAINTAINER_X);
    expect(text).not.toMatch(/[a-z0-9._-]+@[a-z0-9-]+\.[a-z]{2,}/i);
    expect(hrefs().some((href) => href.startsWith("mailto:"))).toBe(false);
  });

  describe("/developers", () => {
    it("renders 500+ characters, the error section ids, the curl example, and no email", () => {
      render(DevelopersPage() as React.ReactElement);
      const text = document.body.textContent ?? "";
      expect(text.length).toBeGreaterThan(500);
      expect(document.querySelector("#error-not-found")).not.toBeNull();
      expect(document.querySelector("section#error-not-found h2")?.textContent).toContain("404");
      expect(document.querySelector("#error-method-not-allowed")).not.toBeNull();
      expect(document.querySelector("pre")?.textContent).toBe(
        "$ curl http://my-sink.tailnet.ts.net:9999/healthz\nok",
      );
      expect(text).toContain(API_NOT_FOUND_DETAIL);
      expect(text).not.toMatch(/[a-z0-9._-]+@[a-z0-9-]+\.[a-z]{2,}/i);
      expect(text).not.toContain("!");
      expect(hrefs().some((href) => href.startsWith("mailto:"))).toBe(false);
    });

    it("links the OpenAPI document, the API catalog, the quickstart, the spec, and releases", () => {
      render(DevelopersPage() as React.ReactElement);
      const links = hrefs();
      expect(links).toContain("https://agentcookie.dev/openapi.json");
      expect(links).toContain("https://agentcookie.dev/.well-known/api-catalog");
      expect(links).toContain(
        "https://github.com/mvanhorn/agentcookie/blob/main/docs/quickstart.md",
      );
      expect(links).toContain(
        "https://github.com/mvanhorn/agentcookie/blob/main/docs/spec-agentcookie-secrets-bus-v1.md",
      );
      expect(links).toContain("https://github.com/mvanhorn/agentcookie/releases");
    });
  });
});
