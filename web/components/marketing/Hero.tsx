// Homepage hero - server component.
//
// Two-line headline in Geist Mono. Tagline below in Geist Sans.
// Copy lives in lib/content/home.ts and traces to the agentcookie
// README intro paragraph.
//
// No CTA inside the hero; the TopNav has the Install button and the
// WhatItSyncs section does the showing.

import { HERO } from "@/lib/content/home";

export function Hero() {
  const [line1, line2] = HERO.headline;
  return (
    <section className="py-24 pb-16 text-left">
      <h1
        className="m-0 mb-6 font-display font-medium text-text-0"
        style={{
          fontSize: "clamp(48px, 7vw, 88px)",
          lineHeight: 1.05,
          letterSpacing: "-0.03em",
        }}
      >
        {line1}
        <br />
        {line2}
      </h1>
      <p className="m-0 max-w-[760px] font-body text-[20px] text-text-1">
        {HERO.tagline}
      </p>
    </section>
  );
}

export default Hero;
