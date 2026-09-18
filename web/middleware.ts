// Accept negotiation for page requests (KTD1). Runs on the Edge
// runtime and imports only the pure parser and the origin constant.
//
// A GET or HEAD that prefers text/markdown is rewritten to the
// Markdown route tree: `/` becomes `/md`, `/about` becomes
// `/md/about`, search preserved. Everything else passes through with
// `Vary: Accept` merged onto whatever Vary is already set and a
// `Link` header advertising the twin (R4), built from SITE_ORIGIN so
// the request's Host, Origin, and forwarded headers never reach a
// response (R15).
//
// Next's built-in trailing-slash and repeated-slash redirects run
// before middleware, so this file never redirects. The matcher keeps
// `_next/*`, `_vercel/*`, `/md` and `/md/*`, `/api` and `/api/*`,
// `/.well-known` and `/.well-known/*`, `/opengraph-image`, and any
// path with a file extension (which covers `/openapi.json`) out of
// the middleware altogether: those trees serve machine documents and
// problem responses, never a page with a Markdown twin.

import { NextResponse, type NextRequest } from "next/server";
import { preferredRepresentation } from "@/lib/negotiate";
import { SITE_ORIGIN } from "@/lib/site";

export const config = {
  matcher: [
    "/((?!_next/|_vercel/|md(?:/|$)|api(?:/|$)|\\.well-known(?:/|$)|opengraph-image(?:/|$)|.*\\.[^/]*$).*)",
  ],
};

function twinPath(pathname: string): string {
  return pathname === "/" ? "/md" : `/md${pathname}`;
}

// Append Accept to an existing Vary value without duplicating it.
// Comparison is case-insensitive; the existing spelling is kept.
export function mergeVary(existing: string | null): string {
  const seen = new Set<string>();
  const values: string[] = [];
  for (const raw of (existing ?? "").split(",")) {
    const value = raw.trim();
    if (value.length === 0) continue;
    const key = value.toLowerCase();
    if (seen.has(key)) continue;
    seen.add(key);
    values.push(value);
  }
  if (!seen.has("accept")) values.push("Accept");
  return values.join(", ");
}

export function middleware(request: NextRequest): NextResponse {
  const { pathname } = request.nextUrl;
  const negotiable = request.method === "GET" || request.method === "HEAD";
  if (
    negotiable &&
    preferredRepresentation(request.headers.get("accept")) === "markdown"
  ) {
    const url = request.nextUrl.clone();
    url.pathname = twinPath(pathname);
    return NextResponse.rewrite(url);
  }

  const response = NextResponse.next();
  response.headers.set("Vary", mergeVary(response.headers.get("Vary")));
  response.headers.set(
    "Link",
    `<${SITE_ORIGIN}${twinPath(pathname)}>; rel="alternate"; type="text/markdown"`,
  );
  return response;
}
