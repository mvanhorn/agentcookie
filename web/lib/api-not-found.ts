// Everything under /api. agentcookie.dev hosts no API: the sink HTTP
// interface runs on the user's tailnet, so every method on every
// path here answers an RFC 9457 problem with status 404 that points
// at /openapi.json and /developers. OPTIONS is a 204 preflight so a
// browser client gets to see the problem body instead of a CORS
// failure. app/api/[[...rest]]/route.ts re-exports the handler.
//
// The body carries the request pathname as `instance` and nothing
// else from the request: no query, no headers, no body.

import { CORS_HEADERS, problemResponse, problemType } from "./problem";

const ALL_METHODS = "GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS";

export const API_NOT_FOUND_DETAIL =
  "agentcookie.dev hosts no API; the sink HTTP interface runs on your tailnet";

export function handleApiRequest(request: Request): Response {
  if (request.method === "OPTIONS") {
    return new Response(null, {
      status: 204,
      headers: {
        Allow: ALL_METHODS,
        ...CORS_HEADERS,
        "Access-Control-Allow-Methods": ALL_METHODS,
        "Access-Control-Allow-Headers": "Accept, Content-Type",
        "Access-Control-Max-Age": "86400",
      },
    });
  }
  return problemResponse(request, {
    type: problemType("error-not-found"),
    title: "Not Found",
    status: 404,
    detail: API_NOT_FOUND_DETAIL,
  });
}

export const GET = handleApiRequest;
export const HEAD = handleApiRequest;
export const POST = handleApiRequest;
export const PUT = handleApiRequest;
export const PATCH = handleApiRequest;
export const DELETE = handleApiRequest;
export const OPTIONS = handleApiRequest;
