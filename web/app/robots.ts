// /robots.txt (R11). Every agent may crawl every page; the sitemap is
// named on the literal production origin (R15), not metadataBase or
// an environment variable, so a preview deployment still points
// crawlers at agentcookie.dev.

import type { MetadataRoute } from "next";
import { SITE_ORIGIN } from "@/lib/site";

export default function robots(): MetadataRoute.Robots {
  return {
    rules: [{ userAgent: "*", allow: "/" }],
    sitemap: `${SITE_ORIGIN}/sitemap.xml`,
  };
}
