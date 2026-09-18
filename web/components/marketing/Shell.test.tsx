// Shell contract (KTD4): synchronous, children only, renders the nav
// logo link, the children, and the footer links inside one wrapper.

// @vitest-environment jsdom

import React from "react";
import { describe, it, expect, afterEach } from "vitest";
import { render, cleanup } from "@testing-library/react";
import { Shell } from "./Shell";
import { FOOTER_LINKS } from "@/lib/content/links";

describe("Shell", () => {
  afterEach(() => {
    cleanup();
  });

  it("renders the child, the nav logo link, and the footer links", () => {
    render(
      <Shell>
        <p>hello from the child</p>
      </Shell>,
    );
    expect(document.body.textContent).toContain("hello from the child");
    expect(document.querySelector("[data-marketing-shell]")).not.toBeNull();
    const hrefs = Array.from(
      document.querySelectorAll<HTMLAnchorElement>("a"),
    ).map((a) => a.getAttribute("href"));
    expect(hrefs).toContain("/");
    for (const link of FOOTER_LINKS) {
      expect(hrefs).toContain(link.href);
    }
  });
});
