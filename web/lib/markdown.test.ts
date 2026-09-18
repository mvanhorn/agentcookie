// Markdown twin contract (KTD2, R1, R4, R5, R15). One handler serves
// /md and /md/<page> from the content modules, and a fixed 404 body
// for everything else.

import { describe, it, expect } from "vitest";
import {
  handleMarkdownRequest,
  renderTwin,
  NOT_FOUND_MARKDOWN,
} from "./markdown";
import { HERO, TERMINAL_COMMANDS } from "./content/home";
import { FEATURES } from "./content/features";
import { FAQS } from "./content/faq";
import { FOOTER_LINKS } from "./content/links";
import { TRUST_PAGES } from "./content/trust";
import { ROUTES, TRUST_ROUTE_LINKS } from "./routes";

const BASE = "http://localhost:3000";
const SITE_ORIGIN = "https://agentcookie.dev";

function get(path: string, init: RequestInit = {}): Response {
  return handleMarkdownRequest(new Request(`${BASE}${path}`, init));
}

describe("rewritten requests resolve by route params, not request.url", () => {
  // After a middleware rewrite, a route handler's request.url still carries
  // the ORIGINAL path (`/about`), while the catch-all params carry the
  // rewritten segments. The route binding passes those segments through.
  it("serves the about twin for an original /about URL with params ['about']", async () => {
    const response = handleMarkdownRequest(new Request(`${BASE}/about`), ["about"]);
    expect(response.status).toBe(200);
    expect(await response.text()).toContain(`# ${TRUST_PAGES.about.title}`);
  });

  it("serves the home twin for an original / URL with empty params", async () => {
    const response = handleMarkdownRequest(new Request(`${BASE}/`), []);
    expect(response.status).toBe(200);
    expect(await response.text()).toContain(HERO.tagline);
  });

  it("returns the fixed 404 for params ['md', 'about']", async () => {
    const response = handleMarkdownRequest(new Request(`${BASE}/md/md/about`), ["md", "about"]);
    expect(response.status).toBe(404);
    expect(await response.text()).toBe(NOT_FOUND_MARKDOWN);
  });

  it("exposes route bindings that read params.path", async () => {
    const mod = await import("./markdown");
    const response = await mod.GET(new Request(`${BASE}/about`), {
      params: Promise.resolve({ path: ["about"] }),
    });
    expect(response.status).toBe(200);
    expect(await response.text()).toContain(`# ${TRUST_PAGES.about.title}`);
  });
});

describe("homepage twin (/md)", () => {
  it("renders the hero, every feature title, and every FAQ question", async () => {
    const response = get("/md");
    expect(response.status).toBe(200);
    const body = await response.text();
    expect(body).toContain(`# ${HERO.headline.join(" ")}`);
    expect(body).toContain(HERO.tagline);
    for (const feature of FEATURES) {
      expect(body).toContain(feature.title);
    }
    for (const faq of FAQS) {
      expect(body).toContain(`### ${faq.question}`);
      for (const para of faq.answer) expect(body).toContain(para);
    }
  });

  it("renders the terminal lines and manifest as fenced blocks", async () => {
    const body = await get("/md").text();
    for (const command of TERMINAL_COMMANDS) {
      expect(body).toContain(`$ ${command}`);
    }
    expect(body).toContain("```toml\n");
    expect(body).toContain('name = "stripe-pp-cli"');
    expect(body).toContain("STRIPE_SECRET_KEY = true");
    expect(body).toContain("cat ~/.agentcookie/secrets/stripe-pp-cli/secrets.env");
  });

  it("renders the footer links as a Markdown list", async () => {
    const body = await get("/md").text();
    for (const link of FOOTER_LINKS) {
      expect(body).toContain(`[${link.label}](${link.href})`);
    }
  });

  it("renders every footer trust-page link (about, contact, privacy) as an absolute URL", async () => {
    const body = await get("/md").text();
    expect(TRUST_ROUTE_LINKS.length).toBeGreaterThan(0);
    for (const route of ROUTES) {
      if (route.path === "/") continue;
      expect(body).toContain(`(${SITE_ORIGIN}${route.path})`);
    }
  });

  it("sets the Markdown, canonical, cache, and robots headers", () => {
    const response = get("/md");
    expect(response.headers.get("content-type")).toBe(
      "text/markdown; charset=utf-8",
    );
    expect(response.headers.get("vary")).toBe("Accept");
    expect(response.headers.get("x-robots-tag")).toBe("noindex");
    expect(response.headers.get("x-content-type-options")).toBe("nosniff");
    expect(response.headers.get("link")).toBe(
      '<https://agentcookie.dev/>; rel="canonical"',
    );
    expect(response.headers.get("cache-control")).toBe(
      "public, s-maxage=300, stale-while-revalidate=86400",
    );
  });

  it("ignores Host, Origin, and forwarded headers when building URLs", async () => {
    const response = get("/md/about?x=1", {
      headers: {
        host: "evil.example",
        origin: "https://evil.example",
        "x-forwarded-host": "evil.example",
        "x-forwarded-proto": "http",
      },
    });
    expect(response.headers.get("link")).toBe(
      '<https://agentcookie.dev/about>; rel="canonical"',
    );
    for (const [, value] of response.headers) {
      expect(value).not.toContain("evil.example");
    }
    expect(await response.text()).not.toContain("evil.example");
  });
});

describe("trust page twins", () => {
  it("/md/about renders the title and section headings", async () => {
    const response = get("/md/about");
    expect(response.status).toBe(200);
    const body = await response.text();
    expect(body).toContain(`# ${TRUST_PAGES.about.title}`);
    expect(body).toContain(TRUST_PAGES.about.description);
    for (const section of TRUST_PAGES.about.sections) {
      expect(body).toContain(`## ${section.heading}`);
      for (const para of section.paragraphs) expect(body).toContain(para);
    }
    for (const link of TRUST_PAGES.about.links) {
      expect(body).toContain(`[${link.label}](${link.href})`);
    }
    expect(response.headers.get("link")).toBe(
      '<https://agentcookie.dev/about>; rel="canonical"',
    );
  });

  it("renders every ROUTES path as an absolute URL in each trust page twin", async () => {
    for (const key of ["about", "contact", "privacy", "developers"] as const) {
      const body = await get(`/md/${key}`).text();
      for (const route of ROUTES) {
        if (route.path === "/") continue;
        expect(body, `${key} twin missing link to ${route.path}`).toContain(
          `(${SITE_ORIGIN}${route.path})`,
        );
      }
    }
  });

  it("renders every route in the table through the same handler", async () => {
    for (const route of ROUTES) {
      const path = route.path === "/" ? "/md" : `/md${route.path}`;
      const response = get(path);
      expect(response.status, path).toBe(200);
      expect(response.headers.get("link")).toBe(
        `<https://agentcookie.dev${route.path}>; rel="canonical"`,
      );
      const body = await response.text();
      expect(body.length).toBeGreaterThan(500);
      expect(body).toBe(renderTwin(route.twin));
    }
  });

  it("/md/contact, /md/privacy, and /md/developers render their page modules", async () => {
    for (const key of ["contact", "privacy", "developers"] as const) {
      const body = await get(`/md/${key}`).text();
      expect(body).toContain(`# ${TRUST_PAGES[key].title}`);
      for (const section of TRUST_PAGES[key].sections) {
        expect(body).toContain(`## ${section.heading}`);
      }
    }
  });

  it("/md/developers renders the curl example as a fenced block and links the documents", async () => {
    const body = await get("/md/developers").text();
    expect(body).toContain("```\n$ curl http://my-sink.tailnet.ts.net:9999/healthz\nok\n```");
    expect(body).toContain(`(${SITE_ORIGIN}/openapi.json)`);
    expect(body).toContain(`(${SITE_ORIGIN}/.well-known/api-catalog)`);
    expect(body).not.toContain("!");
  });
});

describe("Markdown 404", () => {
  it("returns the fixed body for /md/md/about and other unknown paths", async () => {
    for (const path of ["/md/md/about", "/md/nope", "/md/about/extra", "/about"]) {
      const response = get(path);
      expect(response.status, path).toBe(404);
      expect(await response.text()).toBe(NOT_FOUND_MARKDOWN);
      expect(response.headers.get("cache-control")).toBe("no-store");
      expect(response.headers.get("content-type")).toBe(
        "text/markdown; charset=utf-8",
      );
      expect(response.headers.get("x-robots-tag")).toBe("noindex");
      expect(response.headers.get("link")).toBeNull();
    }
  });

  it("never reflects the requested path or query", async () => {
    const response = get("/md/nope%0A%23%20Injected?q=%23%20Also");
    expect(response.status).toBe(404);
    const body = await response.text();
    expect(body.length).toBeGreaterThan(20);
    expect(body).toContain("https://agentcookie.dev/");
    expect(body).toContain("https://agentcookie.dev/sitemap.xml");
    expect(body).toContain("https://agentcookie.dev/llms.txt");
    expect(body).not.toContain("Injected");
    expect(body).not.toContain("Also");
    expect(body).not.toContain("nope");
    expect(body).not.toContain("#");
    expect(body).toMatch(/does not exist/);
  });
});

describe("HEAD", () => {
  it("returns the same headers as GET with an empty body", async () => {
    const getResponse = get("/md");
    const headResponse = get("/md", { method: "HEAD" });
    expect(headResponse.status).toBe(200);
    expect(await headResponse.text()).toBe("");
    for (const [key, value] of getResponse.headers) {
      expect(headResponse.headers.get(key), key).toBe(value);
    }
  });

  it("returns 404 with an empty body for an unknown path", async () => {
    const response = get("/md/nope", { method: "HEAD" });
    expect(response.status).toBe(404);
    expect(await response.text()).toBe("");
    expect(response.headers.get("cache-control")).toBe("no-store");
  });
});
