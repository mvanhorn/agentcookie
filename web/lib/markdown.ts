// Markdown twins (KTD2). One handler serves the whole `/md` tree:
// `/md` is the homepage twin, `/md/<page>` the twin of each page in
// the static route table, and anything else is a fixed 404. The
// route file under app/md is a one-line binding to this module so
// the handler can be tested with a plain Request.
//
// Every string comes from the content modules the HTML pages render,
// so the two representations cannot drift. Every absolute URL is
// built from SITE_ORIGIN (R15); the request is read only for its
// method and pathname.

import { SITE_ORIGIN } from "./site";
import { findRoute, TRUST_ROUTE_LINKS, type TwinKey } from "./routes";
import {
  HERO,
  TERMINAL,
  MANIFEST,
  SINK_SURFACE,
  SECRETS_BUS_CAPTION,
  WHAT_IT_SYNCS,
  FEATURE_GRID,
  FOOTER_LINE,
} from "./content/home";
import { FEATURES } from "./content/features";
import { FAQS, FAQ_HEADING } from "./content/faq";
import { FOOTER_LINKS } from "./content/links";
import { TRUST_PAGES, type TrustPage } from "./content/trust";

const MD_PREFIX = "/md";

const CONTENT_TYPE = "text/markdown; charset=utf-8";
const CACHE_OK = "public, s-maxage=300, stale-while-revalidate=86400";
const CACHE_NOT_FOUND = "no-store";

// Fixed body for every unknown path. It never contains the requested
// path or query, and no `#` at all so a test can prove nothing from
// the URL was reflected as a heading.
export const NOT_FOUND_MARKDOWN = [
  "Not found. This page does not exist on agentcookie.dev.",
  "",
  `Start from the homepage: ${SITE_ORIGIN}/`,
  `Every page is listed in the sitemap: ${SITE_ORIGIN}/sitemap.xml`,
  `A guide to the site for agents: ${SITE_ORIGIN}/llms.txt`,
  "",
].join("\n");

function fence(lines: readonly string[], lang = ""): string {
  return ["```" + lang, ...lines, "```"].join("\n");
}

function linkList(links: readonly { label: string; href: string }[]): string {
  return links.map((link) => `- [${link.label}](${link.href})`).join("\n");
}

// The footer's internal trust-page links (about, contact, privacy),
// rendered here as absolute URLs the way every other link in a
// Markdown twin is (KTD2/R15), so an agent reading the twin in
// isolation never needs to resolve a relative path.
const INTERNAL_TRUST_LINKS = TRUST_ROUTE_LINKS.map((link) => ({
  label: link.label,
  href: `${SITE_ORIGIN}${link.href}`,
}));

function terminalBlock(): string {
  const lines = TERMINAL.lines.map((line) =>
    line.kind === "command" ? `$ ${line.text}` : line.text,
  );
  return fence(lines);
}

function manifestBlock(): string {
  const lines: string[] = [];
  for (const line of MANIFEST.lines) {
    if (line.gapAbove) lines.push("");
    lines.push(line.tokens.map((token) => token.text).join(""));
  }
  return fence(lines, "toml");
}

function sinkSurfaceBlock(): string {
  return fence([
    SINK_SURFACE.comment,
    `$ ${SINK_SURFACE.command}`,
    SINK_SURFACE.output,
  ]);
}

function renderHome(): string {
  const blocks: string[] = [
    `# ${HERO.headline.join(" ")}`,
    HERO.tagline,
    `## ${WHAT_IT_SYNCS.label}`,
    WHAT_IT_SYNCS.caption,
    `### ${TERMINAL.label}`,
    terminalBlock(),
    `${TERMINAL.caption.before}\`${TERMINAL.caption.code}\`${TERMINAL.caption.after}`,
    `### ${MANIFEST.label}`,
    `\`${MANIFEST.filename}\``,
    manifestBlock(),
    sinkSurfaceBlock(),
    SECRETS_BUS_CAPTION,
    `## ${FEATURE_GRID.heading}`,
    FEATURES.map((feature) => `- ${feature.title}: ${feature.body}`).join("\n"),
    FEATURE_GRID.trailer,
    `## ${FAQ_HEADING}`,
  ];
  for (const faq of FAQS) {
    blocks.push(`### ${faq.question}`, ...faq.answer);
  }
  blocks.push(
    "## links",
    linkList(FOOTER_LINKS),
    linkList(INTERNAL_TRUST_LINKS),
    FOOTER_LINE,
  );
  return blocks.join("\n\n") + "\n";
}

function renderTrust(page: TrustPage): string {
  const blocks: string[] = [`# ${page.title}`, page.description];
  for (const section of page.sections) {
    blocks.push(`## ${section.heading}`, ...section.paragraphs);
    if (section.code) blocks.push(fence(section.code));
  }
  blocks.push("## Links", linkList(page.links), linkList(INTERNAL_TRUST_LINKS));
  return blocks.join("\n\n") + "\n";
}

// Every twin is a pure function of module-level content constants, so the
// bodies are rendered once at module load rather than on every request.
const TWIN_MARKDOWN: Readonly<Record<TwinKey, string>> = {
  home: renderHome(),
  about: renderTrust(TRUST_PAGES.about),
  contact: renderTrust(TRUST_PAGES.contact),
  privacy: renderTrust(TRUST_PAGES.privacy),
  developers: renderTrust(TRUST_PAGES.developers),
};

export function renderTwin(twin: TwinKey): string {
  return TWIN_MARKDOWN[twin];
}

// `/md` -> `/`, `/md/about` -> `/about`; anything outside the tree
// (or `/md/md/about`, which maps to the non-page `/md/about`) is
// undefined. No decoding, no normalization: findRoute is exact.
function pagePathFor(pathname: string): string | undefined {
  if (pathname === MD_PREFIX) return "/";
  if (pathname.startsWith(`${MD_PREFIX}/`)) return pathname.slice(MD_PREFIX.length);
  return undefined;
}

function markdownResponse(
  body: string,
  status: number,
  extra: Record<string, string>,
  method: string,
): Response {
  const headers = new Headers({
    "Content-Type": CONTENT_TYPE,
    Vary: "Accept",
    "X-Robots-Tag": "noindex",
    "X-Content-Type-Options": "nosniff",
    ...extra,
  });
  return new Response(method === "HEAD" ? null : body, { status, headers });
}

// After a middleware rewrite, `request.url` in a route handler still names
// the ORIGINAL path (`/about`), while the catch-all `params.path` carries the
// rewritten segments. Callers that have the segments pass them; direct calls
// (tests, or a request that reached `/md/*` without a rewrite) fall back to
// the URL.
export function handleMarkdownRequest(
  request: Request,
  segments?: readonly string[],
): Response {
  const pathname =
    segments === undefined
      ? new URL(request.url).pathname
      : segments.length === 0
        ? MD_PREFIX
        : `${MD_PREFIX}/${segments.join("/")}`;
  const pagePath = pagePathFor(pathname);
  const route = pagePath === undefined ? undefined : findRoute(pagePath);
  if (!route) {
    return markdownResponse(
      NOT_FOUND_MARKDOWN,
      404,
      { "Cache-Control": CACHE_NOT_FOUND },
      request.method,
    );
  }
  return markdownResponse(
    renderTwin(route.twin),
    200,
    {
      "Cache-Control": CACHE_OK,
      Link: `<${SITE_ORIGIN}${route.path}>; rel="canonical"`,
    },
    request.method,
  );
}

type RouteContext = { params: Promise<{ path?: string[] }> };

async function handleRoute(request: Request, context: RouteContext): Promise<Response> {
  const { path } = await context.params;
  return handleMarkdownRequest(request, path ?? []);
}

export const GET = handleRoute;
export const HEAD = handleRoute;
