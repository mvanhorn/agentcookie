// /openapi.json: the OpenAPI 3.1 description of the sink HTTP
// interface. Handlers live in lib/openapi-route.ts so they can be
// tested without this directory.

export { GET, HEAD, OPTIONS, POST, PUT, PATCH, DELETE } from "@/lib/openapi-route";
