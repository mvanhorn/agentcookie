// /developers - server component, synchronous.
//
// Copy lives in lib/content/developers.ts; TrustArticle renders it and
// Shell supplies the nav and footer (KTD4). Metadata carries a
// self-referencing canonical and Open Graph URL (R7). The section ids
// error-not-found and error-method-not-allowed are the targets of the
// RFC 9457 problem `type` URIs the /api and document routes emit.

import { Shell } from "@/components/marketing/Shell";
import { TrustArticle } from "@/components/marketing/TrustArticle";
import { trustMetadata } from "@/lib/trust-metadata";

export const metadata = trustMetadata("developers");

export default function DevelopersPage() {
  return (
    <Shell>
      <TrustArticle pageKey="developers" />
    </Shell>
  );
}
