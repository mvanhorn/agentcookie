// /llms.txt: agent guidance file (R12). The body is a pure render of
// lib/content/llms.ts plus the route table and the links module; the
// handler only sets the content type.

import { describe, it, expect } from "vitest";
import { GET } from "./route";
import { renderLlmsTxt } from "@/lib/content/llms";
import { ROUTES } from "@/lib/routes";
import { LINKS, ISSUES, MAINTAINER_X } from "@/lib/content/links";

const ORIGIN = "https://agentcookie.dev";
const REPO = "https://github.com/mvanhorn/agentcookie";

function section(body: string, heading: string): string {
  const start = body.indexOf(`\n## ${heading}\n`);
  expect(start, `missing section ${heading}`).toBeGreaterThan(-1);
  const rest = body.slice(start + 1);
  const next = rest.indexOf("\n## ", 1);
  return next === -1 ? rest : rest.slice(0, next);
}

describe("renderLlmsTxt", () => {
  const body = renderLlmsTxt();

  it("starts with the H1 and a one-line blockquote", () => {
    const lines = body.split("\n");
    expect(lines[0]).toBe("# agentcookie");
    const quote = lines.find((l) => l.startsWith("> "));
    expect(quote).toBeDefined();
    expect(body.match(/^> /gm)).toHaveLength(1);
  });

  it("carries the guidance, install, reading, main pages, and contact headings", () => {
    for (const heading of [
      "When to use agentcookie",
      "When not to use agentcookie",
      "How to install",
      "How to read this site",
      "Main pages",
      "Contact",
    ]) {
      expect(body).toContain(`\n## ${heading}\n`);
    }
  });

  it("uses the skill file's trigger phrases as when-to-use bullets", () => {
    const when = section(body, "When to use agentcookie");
    for (const phrase of [
      "install agentcookie",
      "set up cookie sync",
      "share my Chrome sessions with my agent box",
      "make my agent log in as me",
    ]) {
      expect(when).toContain(`"${phrase}"`);
    }
  });

  it("names the exclusions as agent triggers", () => {
    const not = section(body, "When not to use agentcookie");
    expect(not).toContain("DBSC");
    expect(not).toContain("Google");
    expect(not).toContain("fingerprint");
    expect(not).toContain("Tailscale");
    expect(not).toContain("Windows");
    expect(not).toContain("consent");
  });

  it("gives the release, go install, and pair-then-sync paths with real command names", () => {
    const install = section(body, "How to install");
    expect(install).toContain("darwin_arm64");
    expect(install).toContain("linux_amd64");
    expect(install).toContain("linux_arm64");
    expect(install).toContain("checksums.txt");
    expect(install).toContain(
      "go install github.com/mvanhorn/agentcookie/cmd/agentcookie@latest"
    );
    expect(install).toContain("agentcookie pair --as source");
    expect(install).toContain("agentcookie pair --as sink");
    expect(install).toContain("agentcookie sink");
    expect(install).toContain("agentcookie source --watch");
  });

  it("explains the Markdown twins and lists all five", () => {
    const how = section(body, "How to read this site");
    expect(how).toContain("hosts no callable API");
    expect(how).not.toContain("no public HTTP API, OpenAPI document");
    expect(how).toContain(`${ORIGIN}/openapi.json`);
    expect(how).toContain("sink HTTP interface you run yourself");
    expect(how).toContain(`${ORIGIN}/developers`);
    expect(how).toContain("Accept: text/markdown");
    expect(how).toContain(`${ORIGIN}/md`);
    expect(how).toContain(`${ORIGIN}/md/about`);
    expect(how).toContain(`${ORIGIN}/md/contact`);
    expect(how).toContain(`${ORIGIN}/md/privacy`);
    expect(how).toContain(`${ORIGIN}/md/developers`);
  });

  it("links every route, the crawler files, and the repo docs as absolute URLs", () => {
    const main = section(body, "Main pages");
    const hrefs = Array.from(main.matchAll(/\]\(([^)]+)\)/g)).map((m) => m[1]);
    expect(hrefs.length).toBeGreaterThan(0);
    for (const href of hrefs) {
      expect(
        href.startsWith(`${ORIGIN}/`) ||
          href === REPO ||
          href.startsWith(`${REPO}/`),
        `not absolute to site or repo: ${href}`
      ).toBe(true);
    }
    for (const route of ROUTES) {
      expect(hrefs).toContain(`${ORIGIN}${route.path}`);
    }
    expect(hrefs).toContain(`${ORIGIN}/developers`);
    expect(hrefs).toContain(`${ORIGIN}/openapi.json`);
    expect(main).toContain(
      `[OpenAPI description of the sink interface](${ORIGIN}/openapi.json)`,
    );
    expect(hrefs).toContain(`${ORIGIN}/sitemap.xml`);
    expect(hrefs).toContain(`${ORIGIN}/llms.txt`);
    // The quickstart, spec, and threat-model docs describe the tool's
    // private tailnet protocol; linking them here made scanners grade
    // the site as if it hosted a public API, so llms.txt points at the
    // repository instead and the docs stay reachable from there.
    expect(hrefs).not.toContain(LINKS.quickstart);
    expect(hrefs).not.toContain(LINKS.secretsBusV1Spec);
    expect(hrefs).not.toContain(LINKS.v2AdoptionSpec);
    expect(hrefs).not.toContain(LINKS.threatModel);
    expect(hrefs).toContain(`${REPO}/blob/main/docs/faq.md`);
    expect(hrefs).toContain(`${REPO}/blob/main/skill/SKILL.md`);
    expect(hrefs).toContain(REPO);
  });

  it("offers GitHub issues and x.com only, no email", () => {
    const contact = section(body, "Contact");
    expect(contact).toContain(ISSUES);
    expect(contact).toContain(MAINTAINER_X);
    expect(body).not.toMatch(/mailto:|@gmail|@agentcookie/i);
  });

  it("never builds a URL from the platform variable and stays under 30,000 characters", () => {
    expect(body.length).toBeLessThan(30_000);
    expect(body).not.toContain("vercel.app");
    expect(body).not.toContain("localhost");
  });
});

describe("GET /llms.txt", () => {
  it("returns the rendered body as text/plain; charset=utf-8", async () => {
    const response = GET();
    expect(response.status).toBe(200);
    expect(response.headers.get("content-type")).toBe(
      "text/plain; charset=utf-8"
    );
    expect(await response.text()).toBe(renderLlmsTxt());
  });
});
