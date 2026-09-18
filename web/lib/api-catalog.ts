// /.well-known/api-catalog (RFC 9727): a linkset naming the one API
// this site describes. app/.well-known/api-catalog/route.ts re-exports
// the handlers so they can be tested with a plain Request.
//
// Anchor choice. RFC 9727 wants each linkset member's anchor to be a
// URI that identifies the API. The sink's server URL in the OpenAPI
// document is a template (http://{host}:{port}) that differs for every
// user's tailnet, so it is not a valid anchor. The OpenAPI document's
// own URL on this origin is a stable identifier for the API, so the
// anchor is https://agentcookie.dev/openapi.json (RFC 9727 section
// 4.1 allows any URI that identifies the API; it need not be the
// API's base URL). service-desc then points at that same document and
// service-doc at the human page.

import { OPENAPI_CONTENT_TYPE } from "./openapi-route";
import { SITE_ORIGIN } from "./site";
import { createStaticDocument } from "./static-document";

export const API_CATALOG_CONTENT_TYPE =
  'application/linkset+json; profile="https://www.rfc-editor.org/info/rfc9727"';

type LinkTarget = {
  href: string;
  type: string;
  title?: string;
};

export type ApiCatalog = {
  linkset: readonly {
    anchor: string;
    "service-desc": readonly LinkTarget[];
    "service-doc": readonly LinkTarget[];
  }[];
};

export const API_CATALOG: ApiCatalog = {
  linkset: [
    {
      anchor: `${SITE_ORIGIN}/openapi.json`,
      "service-desc": [
        {
          href: `${SITE_ORIGIN}/openapi.json`,
          type: OPENAPI_CONTENT_TYPE.split(";")[0],
          title: "OpenAPI 3.1 description of the agentcookie sink HTTP interface",
        },
      ],
      "service-doc": [
        {
          href: `${SITE_ORIGIN}/developers`,
          type: "text/html",
          title: "Developer notes: where the sink interface runs and how pairing establishes trust",
        },
      ],
    },
  ],
};

export const API_CATALOG_JSON = JSON.stringify(API_CATALOG, null, 2) + "\n";

export const { etag, GET, HEAD, OPTIONS, POST, PUT, PATCH, DELETE } =
  createStaticDocument({
    body: API_CATALOG_JSON,
    contentType: API_CATALOG_CONTENT_TYPE,
  });
