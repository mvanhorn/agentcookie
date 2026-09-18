// Branded HTML 404 (R5): rendered inside Shell, linking home and the
// quickstart.

// @vitest-environment jsdom

import React from "react";
import { describe, it, expect, afterEach } from "vitest";
import { render, cleanup } from "@testing-library/react";
import NotFound from "./not-found";
import { LINKS } from "@/lib/content/links";

describe("not-found page", () => {
  afterEach(() => {
    cleanup();
  });

  it("renders inside the shell with home and quickstart links", () => {
    render(NotFound() as React.ReactElement);
    expect(document.querySelector("[data-marketing-shell]")).not.toBeNull();
    const main = document.querySelector("main");
    expect(main).not.toBeNull();
    const hrefs = Array.from(
      main!.querySelectorAll<HTMLAnchorElement>("a"),
    ).map((a) => a.getAttribute("href"));
    expect(hrefs).toContain("/");
    expect(hrefs).toContain(LINKS.quickstart);
    expect(document.body.textContent).toContain("404");
  });
});
