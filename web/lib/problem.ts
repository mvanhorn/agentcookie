// RFC 9457 problem details. One builder for every JSON error the site
// emits (the /api catch-all's 404 and the read-only document routes'
// 405), so the shape, the media type, and the headers cannot drift.
//
// The `type` URI resolves to a section of /developers that explains
// the error. `instance` is the request pathname only: the query is
// never reflected, and nothing else from the request reaches the body.

import { SITE_ORIGIN } from "./site";

export const PROBLEM_CONTENT_TYPE = "application/problem+json; charset=utf-8";

export const CORS_HEADERS: Readonly<Record<string, string>> = {
  "Access-Control-Allow-Origin": "*",
  "Access-Control-Expose-Headers": "ETag, Allow",
};

export type ProblemDetails = {
  type: string;
  title: string;
  status: number;
  detail: string;
  instance: string;
  // Extension members (RFC 9457 section 3.2): where to go instead.
  links: {
    openapi: string;
    developers: string;
  };
};

export const PROBLEM_LINKS = {
  openapi: `${SITE_ORIGIN}/openapi.json`,
  developers: `${SITE_ORIGIN}/developers`,
} as const;

export function problemType(fragment: "error-not-found" | "error-method-not-allowed"): string {
  return `${SITE_ORIGIN}/developers#${fragment}`;
}

// The request's pathname, without search or hash. `new URL` on a
// route handler's request.url always yields a parsed pathname, so a
// query string cannot leak through here.
export function pathnameOf(request: Request): string {
  return new URL(request.url).pathname;
}

export function problemResponse(
  request: Request,
  problem: Omit<ProblemDetails, "instance" | "links">,
  extraHeaders: Readonly<Record<string, string>> = {},
): Response {
  const body: ProblemDetails = {
    ...problem,
    instance: pathnameOf(request),
    links: PROBLEM_LINKS,
  };
  const headers = new Headers({
    "Content-Type": PROBLEM_CONTENT_TYPE,
    "Cache-Control": "no-store",
    "X-Content-Type-Options": "nosniff",
    "X-Robots-Tag": "noindex",
    ...CORS_HEADERS,
    ...extraHeaders,
  });
  return new Response(request.method === "HEAD" ? null : JSON.stringify(body), {
    status: problem.status,
    headers,
  });
}
