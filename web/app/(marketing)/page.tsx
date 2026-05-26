// Marketing homepage - server component.
//
// Composition: TopNav -> Hero -> WhatItSyncs -> FeatureGrid -> Footer.
//
// Dark surface throughout. No `"use client"` anywhere in this tree:
// the static HTML returned to a non-JS fetch (and to any LLM agent
// curling the page) must contain the full hero copy, terminal demo,
// feature list, and links.
//
// Animations are CSS-only: scroll-driven reveal on each tile, and a
// keyframe-typed terminal sequence. Reduced-motion users get the
// final state instantly (see app/globals.css).

import { TopNav } from "@/components/marketing/TopNav";
import { Hero } from "@/components/marketing/Hero";
import { WhatItSyncs } from "@/components/marketing/WhatItSyncs";
import { FeatureGrid } from "@/components/marketing/FeatureGrid";
import { Footer } from "@/components/marketing/Footer";

export default function MarketingHome() {
  return (
    <div
      data-marketing-shell
      className="mx-auto min-h-screen w-full max-w-[1280px] bg-bg-0 px-12 text-text-0"
    >
      <TopNav />
      <Hero />
      <WhatItSyncs />
      <FeatureGrid />
      <Footer />
    </div>
  );
}
