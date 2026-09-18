// Metadata for the trust pages. Each gets a self-referencing
// canonical (R7) and an Open Graph URL, both relative to the
// metadataBase set in app/layout.tsx, so preview deployments still
// point at agentcookie.dev. Title and description come from the copy
// module; the route path comes from lib/routes.ts.

import type { Metadata } from "next";
import { TRUST_PAGES, type TrustKey } from "@/lib/content/trust";
import { SITE_NAME } from "@/lib/content/home";
import type { RoutePath } from "@/lib/routes";

export function trustMetadata(pageKey: TrustKey): Metadata {
  const page = TRUST_PAGES[pageKey];
  const path: RoutePath = `/${pageKey}`;
  return {
    title: page.title,
    description: page.description,
    alternates: { canonical: path },
    openGraph: {
      url: path,
      type: "website",
      siteName: SITE_NAME,
      title: page.title,
      description: page.description,
    },
  };
}
