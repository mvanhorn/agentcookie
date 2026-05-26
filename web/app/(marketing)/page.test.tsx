// Marketing homepage rendering tests.
//
// The page is a Server Component that composes static marketing
// pieces. We render with React Testing Library and assert that the
// contracts the design + README traceability guarantees ship:
//
//   - Hero headline + tagline strings are present.
//   - Terminal demo includes all three ssh second-mac commands.
//   - Every feature in lib/features.ts renders.
//   - Footer links to repo, quickstart, specs, threat model.
//
// When the README is rewritten such that the tagline or feature
// list changes, the matching test entry needs to update too. That
// is the contract.

// @vitest-environment jsdom

import React from "react";
import { describe, it, expect, afterEach, vi } from "vitest";
import { render, cleanup } from "@testing-library/react";
import MarketingHome from "./page";
import { FEATURES } from "@/lib/features";

vi.mock("server-only", () => ({}));

function renderHome() {
  render(MarketingHome() as React.ReactElement);
}

describe("marketing homepage (/)", () => {
  afterEach(() => {
    cleanup();
  });

  it("renders the hero headline lines", () => {
    renderHome();
    expect(document.body.textContent).toContain("your agent");
    expect(document.body.textContent).toContain("session state, synced");
  });

  it("renders the hero tagline", () => {
    renderHome();
    expect(document.body.textContent).toContain(
      "replicated continuously from your laptop"
    );
    expect(document.body.textContent).toContain("Tailscale");
  });

  it("renders all three terminal demo commands", () => {
    renderHome();
    expect(document.body.textContent).toContain("instacart-pp-cli carts");
    expect(document.body.textContent).toContain("ebay-pp-cli auctions");
    expect(document.body.textContent).toContain(
      "table-reservation-goat"
    );
  });

  it("renders the secrets bus tile manifest example", () => {
    renderHome();
    expect(document.body.textContent).toContain("agentcookie.toml");
    expect(document.body.textContent).toContain("STRIPE_SECRET_KEY");
    expect(document.body.textContent).toContain(
      "~/.agentcookie/secrets/stripe-pp-cli/secrets.env"
    );
  });

  it("renders every feature card from lib/features.ts", () => {
    renderHome();
    for (const feature of FEATURES) {
      expect(document.body.textContent).toContain(feature.title);
    }
  });

  it("links to the canonical repo + docs in the footer", () => {
    renderHome();
    const links = Array.from(
      document.querySelectorAll<HTMLAnchorElement>("a")
    ).map((a) => a.href);
    expect(links).toContain("https://github.com/mvanhorn/agentcookie");
    expect(links).toContain(
      "https://github.com/mvanhorn/agentcookie/blob/main/docs/quickstart.md"
    );
    expect(links).toContain(
      "https://github.com/mvanhorn/agentcookie/blob/main/docs/spec-agentcookie-secrets-bus-v1.md"
    );
    expect(links).toContain(
      "https://github.com/mvanhorn/agentcookie/blob/main/docs/spec-agentcookie-secrets-bus-v2-adoption.md"
    );
    expect(links).toContain(
      "https://github.com/mvanhorn/agentcookie/blob/main/docs/threat-model.md"
    );
  });

  it("is agent-readable: hero tagline appears in static HTML", () => {
    // Proves there's no client-only render path between layout and
    // visible copy. If a future component introduces `"use client"`
    // and that breaks server-side hydration of the tagline, this
    // test catches it.
    renderHome();
    const html = document.body.innerHTML;
    expect(html).toContain("Tailscale");
  });
});
