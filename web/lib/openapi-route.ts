// /openapi.json handlers. The document in lib/openapi.ts is rendered
// to JSON once at module load; app/openapi.json/route.ts re-exports
// these so the handlers can be tested with a plain Request.

import { OPENAPI } from "./openapi";
import { createStaticDocument } from "./static-document";

export const OPENAPI_CONTENT_TYPE =
  "application/vnd.oai.openapi+json; charset=utf-8";

export const OPENAPI_JSON = JSON.stringify(OPENAPI, null, 2) + "\n";

export const { etag, GET, HEAD, OPTIONS, POST, PUT, PATCH, DELETE } =
  createStaticDocument({
    body: OPENAPI_JSON,
    contentType: OPENAPI_CONTENT_TYPE,
  });
