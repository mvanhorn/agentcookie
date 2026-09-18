// Static route table. The only list of the site's pages: the sitemap,
// the footer, llms.txt, and the Markdown handler all read it. Each
// entry owns its own `updatedAt` (KTD10), so a copy change bumps the
// date here and every consumer follows.
//
// `twin` names the renderer in lib/markdown.ts that produces the
// page's Markdown twin; the handler resolves a request path through
// findRoute and dispatches on that key.

import { SITE_TITLE } from "./content/home";
import { TRUST_PAGES } from "./content/trust";

export type TwinKey = "home" | "about" | "contact" | "privacy" | "developers";

export type RoutePath = "/" | "/about" | "/contact" | "/privacy" | "/developers";

export type StaticRoute = {
  path: RoutePath;
  title: string;
  // ISO calendar date (YYYY-MM-DD) of the last copy change.
  updatedAt: string;
  twin: TwinKey;
};

export const ROUTES: readonly StaticRoute[] = [
  {
    path: "/",
    title: SITE_TITLE,
    updatedAt: "2026-09-17",
    twin: "home",
  },
  {
    path: "/about",
    title: TRUST_PAGES.about.title,
    updatedAt: "2026-09-17",
    twin: "about",
  },
  {
    path: "/contact",
    title: TRUST_PAGES.contact.title,
    updatedAt: "2026-09-17",
    twin: "contact",
  },
  {
    path: "/privacy",
    title: TRUST_PAGES.privacy.title,
    updatedAt: "2026-09-17",
    twin: "privacy",
  },
  {
    path: "/developers",
    title: TRUST_PAGES.developers.title,
    updatedAt: "2026-09-18",
    twin: "developers",
  },
] as const;

// Exact-match lookup. No normalization: "/about/" and "/md/about" are
// not pages and resolve to undefined.
export function findRoute(path: string): StaticRoute | undefined {
  return ROUTES.find((route) => route.path === path);
}

export type RouteLink = { href: RoutePath; label: string };

// The site's internal trust-page links (everything but the homepage
// itself), derived once from ROUTES so the footer and the Markdown
// twins render the same set and cannot drift from each other.
export const TRUST_ROUTE_LINKS: readonly RouteLink[] = ROUTES.filter(
  (route) => route.path !== "/",
).map((route) => ({
  href: route.path,
  label: route.path.slice(1),
}));
