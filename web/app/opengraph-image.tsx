import { ImageResponse } from "next/og";
import type { CSSProperties } from "react";

// Dynamic OG image rendered by Next.js at request time. Style mirrors
// printingpress.dev's letterpress palette (paper-cream background,
// ink-black headline, ink-mute body, ink-pink registration marks) so
// the agentcookie share card lands on the same visual family rather
// than a generic open-card icon.
//
// The site itself runs dark; the share image runs light by design —
// X / LinkedIn / Slack feeds are light backgrounds, so a cream card
// stands out where a dark-on-dark card would visually disappear.

export const alt =
  "agentcookie — your agent's session state, synced.";
export const size = { width: 1200, height: 630 };
export const contentType = "image/png";

const PAPER_CREAM = "#F4EFE6";
const PAPER_DEEP = "#EDE6D8";
const INK_BLACK = "#1B1816";
const INK_MUTE = "#5A524A";
const INK_PINK = "#E5006D";
const INK_GREEN = "#1F6B3A";

export default async function OpengraphImage() {
  return new ImageResponse(
    (
      <div
        style={{
          display: "flex",
          flexDirection: "column",
          width: "100%",
          height: "100%",
          backgroundColor: PAPER_CREAM,
          paddingTop: 88,
          paddingBottom: 88,
          paddingLeft: 96,
          paddingRight: 96,
          position: "relative",
        }}
      >
        <RegistrationMark position={{ top: 36, left: 36 }} />
        <RegistrationMark position={{ top: 36, right: 36 }} />
        <RegistrationMark position={{ bottom: 36, left: 36 }} />
        <RegistrationMark position={{ bottom: 36, right: 36 }} />

        {/* Eyebrow: live status + domain */}
        <div
          style={{
            display: "flex",
            alignItems: "center",
            color: INK_MUTE,
            fontSize: 24,
            marginBottom: 32,
          }}
        >
          <div
            style={{
              width: 12,
              height: 12,
              borderRadius: 999,
              backgroundColor: INK_GREEN,
              marginRight: 16,
            }}
          />
          <div>agentcookie.dev</div>
        </div>

        {/* Display headline */}
        <div
          style={{
            display: "flex",
            color: INK_BLACK,
            fontSize: 116,
            lineHeight: 1.0,
            marginBottom: 32,
          }}
        >
          agentcookie.
        </div>

        {/* Tagline */}
        <div
          style={{
            display: "flex",
            color: INK_BLACK,
            fontSize: 50,
            lineHeight: 1.18,
            maxWidth: 1000,
            marginBottom: 24,
          }}
        >
          Your agent&apos;s session state, synced.
        </div>

        {/* Subline */}
        <div
          style={{
            display: "flex",
            color: INK_MUTE,
            fontSize: 26,
            lineHeight: 1.35,
            maxWidth: 1000,
          }}
        >
          Cookies and per-CLI secrets, replicated continuously to the
          Mac your agent runs on. Encrypted over Tailscale.
        </div>

        {/* Footer install line */}
        <div
          style={{
            display: "flex",
            alignItems: "center",
            position: "absolute",
            left: 96,
            bottom: 88,
            color: INK_BLACK,
            fontSize: 24,
            backgroundColor: PAPER_DEEP,
            paddingTop: 16,
            paddingBottom: 16,
            paddingLeft: 24,
            paddingRight: 24,
            borderWidth: 1,
            borderStyle: "solid",
            borderColor: INK_BLACK,
          }}
        >
          <div style={{ color: INK_PINK, marginRight: 16 }}>$</div>
          <div>agentcookie wizard install</div>
        </div>
      </div>
    ),
    size,
  );
}

interface RegistrationMarkProps {
  position: Pick<CSSProperties, "top" | "bottom" | "left" | "right">;
}

function RegistrationMark({ position }: RegistrationMarkProps) {
  return (
    <div
      style={{
        ...position,
        position: "absolute",
        width: 18,
        height: 18,
        borderRadius: 999,
        backgroundColor: INK_PINK,
        display: "flex",
      }}
    />
  );
}
