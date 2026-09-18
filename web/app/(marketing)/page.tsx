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
//
// The JSON-LD identity graph (R8) is a constant serialized with `<`
// escaped (KTD6); the native script tag keeps it in the static HTML.
// Page metadata restates openGraph in full because Next replaces the
// nested object rather than merging it with the layout's.

import type { Metadata } from "next";
import { Shell } from "@/components/marketing/Shell";
import { Hero } from "@/components/marketing/Hero";
import { WhatItSyncs } from "@/components/marketing/WhatItSyncs";
import { FeatureGrid } from "@/components/marketing/FeatureGrid";
import { FAQ } from "@/components/marketing/FAQ";
import { GRAPH, serializeJsonLd } from "@/lib/jsonld";
import { SITE_TITLE, SITE_DESCRIPTION } from "@/lib/content/home";
import { pageMetadata } from "@/lib/trust-metadata";

export const metadata: Metadata = pageMetadata({
  path: "/",
  title: SITE_TITLE,
  description: SITE_DESCRIPTION,
});

export default function MarketingHome() {
  return (
    <Shell>
      <script
        type="application/ld+json"
        dangerouslySetInnerHTML={{ __html: serializeJsonLd(GRAPH) }}
      />
      <Hero />
      <WhatItSyncs />
      <FeatureGrid />
      <FAQ />
    </Shell>
  );
}
