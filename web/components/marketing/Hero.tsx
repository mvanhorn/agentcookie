// Homepage hero - server component.
//
// Two-line headline in Geist Mono. Tagline below in Geist Sans.
// Copy traces directly to the agentcookie README intro paragraph:
//
//   "agentcookie keeps your second Mac's session state - Chrome
//    cookies, per-CLI bearer tokens, API keys, and the auth blobs
//    your tools persist next to them - in sync with your first Mac's"
//
// No CTA inside the hero; the TopNav has the Install button and the
// WhatItSyncs section does the showing.

export function Hero() {
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
        your agent&apos;s
        <br />
        session state, synced
      </h1>
      <p className="m-0 max-w-[760px] font-body text-[20px] text-text-1">
        cookies, bearer tokens, and per-CLI auth blobs replicated
        continuously from your laptop to the Mac your agent runs on.
        encrypted over Tailscale, zero per-site auth ceremony.
      </p>
    </section>
  );
}

export default Hero;
