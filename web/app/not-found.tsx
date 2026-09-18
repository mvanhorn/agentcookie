// Branded HTML 404 (R5). Rendered inside Shell so the nav and footer
// stay put, with a way home and a way into the docs.

import type { Metadata } from "next";
import { Shell } from "@/components/marketing/Shell";
import { LINKS } from "@/lib/content/links";

export const metadata: Metadata = {
  title: "Page not found",
};

export default function NotFound() {
  return (
    <Shell>
      <main className="py-24 pb-16 text-left">
        <p className="m-0 mb-4 font-display text-sm text-text-2">404</p>
        <h1
          className="m-0 mb-6 font-display font-medium text-text-0"
          style={{
            fontSize: "clamp(36px, 5vw, 64px)",
            lineHeight: 1.05,
            letterSpacing: "-0.03em",
          }}
        >
          page not found
        </h1>
        <p className="m-0 mb-8 max-w-[640px] font-body text-[18px] text-text-1">
          nothing is synced to this address. the homepage lists every page,
          and the quickstart is the fastest way to a working install.
        </p>
        <p className="m-0 flex flex-wrap gap-6 font-body text-sm">
          <a href="/" className="text-text-1 hover:text-text-0">
            home
          </a>
          <a href={LINKS.quickstart} className="text-text-1 hover:text-text-0">
            quickstart <span className="font-display text-text-2">↗</span>
          </a>
        </p>
      </main>
    </Shell>
  );
}
