// Marketing homepage - server component.
//
// Composition: Shell (TopNav ... Footer) around
// Hero -> WhatItSyncs -> FeatureGrid -> FAQ.
//
// Dark surface throughout. No `"use client"` anywhere in this tree:
// the static HTML returned to a non-JS fetch (and to any LLM agent
// curling the page) must contain the full hero copy, terminal demo,
// feature list, and links.
//
// Animations are CSS-only: scroll-driven reveal on each tile, and a
// keyframe-typed terminal sequence. Reduced-motion users get the
// final state instantly (see app/globals.css).

import { Shell } from "@/components/marketing/Shell";
import { Hero } from "@/components/marketing/Hero";
import { WhatItSyncs } from "@/components/marketing/WhatItSyncs";
import { FeatureGrid } from "@/components/marketing/FeatureGrid";
import { FAQ } from "@/components/marketing/FAQ";

export default function MarketingHome() {
  return (
    <Shell>
      <Hero />
      <WhatItSyncs />
      <FeatureGrid />
      <FAQ />
    </Shell>
  );
}
