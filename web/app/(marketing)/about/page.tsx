// /about - server component, synchronous.
//
// Copy lives in lib/content/trust.ts; TrustArticle renders it and
// Shell supplies the nav and footer (KTD4). Metadata carries a
// self-referencing canonical and Open Graph URL (R7).

import { Shell } from "@/components/marketing/Shell";
import { TrustArticle } from "@/components/marketing/TrustArticle";
import { trustMetadata } from "@/lib/trust-metadata";

export const metadata = trustMetadata("about");

export default function AboutPage() {
  return (
    <Shell>
      <TrustArticle pageKey="about" />
    </Shell>
  );
}
