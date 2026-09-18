// /api and everything beneath it: a fixed RFC 9457 404, because this
// domain hosts no API. Handler lives in lib/api-not-found.ts so it can
// be tested without this directory.

export { GET, HEAD, OPTIONS, POST, PUT, PATCH, DELETE } from "@/lib/api-not-found";
