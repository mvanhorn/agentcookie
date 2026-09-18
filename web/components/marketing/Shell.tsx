// Page shell - server component, synchronous, children only.
//
// Carries the marketing wrapper, TopNav, and Footer for every page.
// It never reads headers() or cookies(), so a page test can call the
// page function directly and still find the nav and footer links in
// the render tree (KTD4). Not a route-group layout on purpose: that
// would pull the footer out of the tested render.

import type { ReactNode } from "react";
import { TopNav } from "@/components/marketing/TopNav";
import { Footer } from "@/components/marketing/Footer";

export function Shell({ children }: { children: ReactNode }) {
  return (
    <div
      data-marketing-shell
      className="mx-auto min-h-screen w-full max-w-[1280px] bg-bg-0 px-12 text-text-0"
    >
      <TopNav />
      {children}
      <Footer />
    </div>
  );
}

export default Shell;
