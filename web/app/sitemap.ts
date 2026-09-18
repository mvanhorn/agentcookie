// /sitemap.xml (R11). One entry per static route, each with the
// route's own `updatedAt` as lastmod (KTD10), on the literal
// production origin (R15).

import type { MetadataRoute } from "next";
import { ROUTES } from "@/lib/routes";
import { SITE_ORIGIN } from "@/lib/site";

export default function sitemap(): MetadataRoute.Sitemap {
  return ROUTES.map((route) => ({
    url: `${SITE_ORIGIN}${route.path}`,
    lastModified: route.updatedAt,
  }));
}
