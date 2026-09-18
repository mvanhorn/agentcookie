// JSON-LD identity graph for the homepage (R8, KTD6). A trusted
// constant built from the content and links modules; no request
// value ever enters it. Rendered with a native <script> tag through
// serializeJsonLd, which escapes `<` so no string in the graph can
// close that tag.
//
// Contact channels are GitHub issues and x.com/mvanhorn only: no
// `email`, no `address` (product contract).

import { SITE_NAME, SITE_DESCRIPTION } from "@/lib/content/home";
import { GITHUB, ISSUES, MAINTAINER_X } from "@/lib/content/links";
import { SITE_ORIGIN } from "@/lib/site";

const HOME_URL = `${SITE_ORIGIN}/`;
const ORGANIZATION_ID = `${SITE_ORIGIN}/#organization`;
const SOFTWARE_ID = `${SITE_ORIGIN}/#software`;
const WEBSITE_ID = `${SITE_ORIGIN}/#website`;

type Ref = { "@id": string };

type ContactPoint = {
  "@type": "ContactPoint";
  contactType: string;
  url: string;
  availableLanguage: readonly string[];
};

type Organization = {
  "@type": "Organization";
  "@id": string;
  name: string;
  url: string;
  sameAs: readonly string[];
  contactPoint: readonly ContactPoint[];
};

type SoftwareApplication = {
  "@type": "SoftwareApplication";
  "@id": string;
  name: string;
  description: string;
  url: string;
  applicationCategory: string;
  operatingSystem: string;
  offers: { "@type": "Offer"; price: string; priceCurrency: string };
  license: string;
  author: Ref;
  downloadUrl: string;
};

type WebSite = {
  "@type": "WebSite";
  "@id": string;
  url: string;
  name: string;
  publisher: Ref;
  about: Ref;
};

export type JsonLdGraph = {
  "@context": "https://schema.org";
  "@graph": readonly [Organization, SoftwareApplication, WebSite];
};

export const GRAPH: JsonLdGraph = {
  "@context": "https://schema.org",
  "@graph": [
    {
      "@type": "Organization",
      "@id": ORGANIZATION_ID,
      name: SITE_NAME,
      url: HOME_URL,
      sameAs: [GITHUB, MAINTAINER_X],
      contactPoint: [
        {
          "@type": "ContactPoint",
          contactType: "technical support",
          url: ISSUES,
          availableLanguage: ["English"],
        },
      ],
    },
    {
      "@type": "SoftwareApplication",
      "@id": SOFTWARE_ID,
      name: SITE_NAME,
      description: SITE_DESCRIPTION,
      url: HOME_URL,
      applicationCategory: "DeveloperApplication",
      operatingSystem: "macOS (source); Linux or macOS (sink)",
      offers: { "@type": "Offer", price: "0", priceCurrency: "USD" },
      license: "https://spdx.org/licenses/MIT.html",
      author: { "@id": ORGANIZATION_ID },
      downloadUrl: `${GITHUB}/releases`,
    },
    {
      "@type": "WebSite",
      "@id": WEBSITE_ID,
      url: HOME_URL,
      name: SITE_NAME,
      publisher: { "@id": ORGANIZATION_ID },
      about: { "@id": SOFTWARE_ID },
    },
  ],
};

// JSON.stringify with every `<` replaced by its JSON unicode escape.
// The output is still valid JSON and parses back to the input; it
// just can never contain "</script>".
export function serializeJsonLd(value: unknown): string {
  return JSON.stringify(value).replace(/</g, "\\u003c");
}
