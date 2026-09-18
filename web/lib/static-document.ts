// Read-only machine documents (/openapi.json, /.well-known/api-catalog).
//
// One factory turns a body and a media type into the full set of
// route handlers: GET and HEAD with a weak ETag and If-None-Match
// 304, OPTIONS as a 204 CORS preflight, and every writing method as
// an RFC 9457 405. The body and the ETag are computed once when the
// module loads; nothing here reads the request beyond its method,
// its If-None-Match header, and (for the 405 instance) its pathname.
//
// Every document is public data, so `Access-Control-Allow-Origin: *`
// is on every response and a shared cache may hold it for an hour.

import { createHash } from "node:crypto";
import { CORS_HEADERS, problemResponse, problemType } from "./problem";

const READ_METHODS = "GET, HEAD, OPTIONS";
const CACHE_CONTROL = "public, s-maxage=3600";
const PREFLIGHT_HEADERS: Readonly<Record<string, string>> = {
  ...CORS_HEADERS,
  "Access-Control-Allow-Methods": READ_METHODS,
  "Access-Control-Allow-Headers": "Accept, If-None-Match",
  "Access-Control-Max-Age": "86400",
};

export type StaticDocument = {
  body: string;
  contentType: string;
};

export type DocumentHandler = (request: Request) => Response;

export type StaticDocumentHandlers = {
  etag: string;
  GET: DocumentHandler;
  HEAD: DocumentHandler;
  OPTIONS: DocumentHandler;
  POST: DocumentHandler;
  PUT: DocumentHandler;
  PATCH: DocumentHandler;
  DELETE: DocumentHandler;
};

// Weak validator: the bytes are canonical JSON rendered at build
// time, but a proxy may still recompress or reformat them, and a
// byte-equal body is not what a client cares about anyway.
export function weakEtag(body: string): string {
  const digest = createHash("sha256").update(body, "utf8").digest("hex");
  return `W/"${digest.slice(0, 32)}"`;
}

// RFC 9110 section 13.1.2: If-None-Match is a list of entity tags or
// `*`, compared weakly (the `W/` prefix is ignored on both sides).
export function ifNoneMatchMatches(header: string | null, etag: string): boolean {
  if (header === null) return false;
  const opaque = (tag: string) => tag.trim().replace(/^W\//, "");
  const ours = opaque(etag);
  return header
    .split(",")
    .map((tag) => tag.trim())
    .some((tag) => tag === "*" || (tag.length > 0 && opaque(tag) === ours));
}

export function createStaticDocument(document: StaticDocument): StaticDocumentHandlers {
  const { body, contentType } = document;
  const etag = weakEtag(body);

  const okHeaders: Readonly<Record<string, string>> = {
    "Content-Type": contentType,
    "Cache-Control": CACHE_CONTROL,
    "X-Content-Type-Options": "nosniff",
    ETag: etag,
    ...CORS_HEADERS,
  };

  const notModifiedHeaders: Readonly<Record<string, string>> = {
    "Cache-Control": CACHE_CONTROL,
    ETag: etag,
    ...CORS_HEADERS,
  };

  const read: DocumentHandler = (request) => {
    if (ifNoneMatchMatches(request.headers.get("if-none-match"), etag)) {
      return new Response(null, { status: 304, headers: notModifiedHeaders });
    }
    return new Response(request.method === "HEAD" ? null : body, {
      status: 200,
      headers: okHeaders,
    });
  };

  const preflight: DocumentHandler = () =>
    new Response(null, {
      status: 204,
      headers: { Allow: READ_METHODS, ...PREFLIGHT_HEADERS },
    });

  const refuse: DocumentHandler = (request) =>
    problemResponse(
      request,
      {
        type: problemType("error-method-not-allowed"),
        title: "Method Not Allowed",
        status: 405,
        detail: `This document is read-only; use ${READ_METHODS}.`,
      },
      { Allow: READ_METHODS },
    );

  return {
    etag,
    GET: read,
    HEAD: read,
    OPTIONS: preflight,
    POST: refuse,
    PUT: refuse,
    PATCH: refuse,
    DELETE: refuse,
  };
}
