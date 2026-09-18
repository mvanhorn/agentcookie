// Metadata for the trust pages. Each gets a self-referencing
// canonical (R7) and an Open Graph URL, both relative to the
// metadataBase set in app/layout.tsx, so preview deployments still
// point at agentcookie.dev. Title and description come from the copy
// module; the route path comes from lib/routes.ts.
//
// Next replaces a page's openGraph and twitter objects wholesale
// rather than merging them with the layout's, which also drops the
// image the app/opengraph-image.tsx file convention attaches at the
// root. Every page therefore names that image explicitly so og:image
// and twitter:image survive on every route.

import type { Metadata } from "next";
import { TRUST_PAGES, type TrustKey } from "@/lib/content/trust";
import { SITE_NAME } from "@/lib/content/home";
import type { RoutePath } from "@/lib/routes";
import { OG_IMAGE } from "@/lib/og";

export function pageMetadata(input: {
  path: RoutePath;
  title: string;
  description: string;
}): Metadata {
  return {
    title: input.title,
    description: input.description,
    alternates: { canonical: input.path },
    openGraph: {
      url: input.path,
      type: "website",
      siteName: SITE_NAME,
      title: input.title,
      description: input.description,
      images: [OG_IMAGE],
    },
    twitter: {
      card: "summary_large_image",
      title: input.title,
      description: input.description,
      images: [OG_IMAGE],
    },
  };
}

export function trustMetadata(pageKey: TrustKey): Metadata {
  const page = TRUST_PAGES[pageKey];
  return pageMetadata({
    path: `/${pageKey}`,
    title: page.title,
    description: page.description,
  });
}
