import React from "react";

// Homepage SecretsBusTile - server component.
//
// Right-hand companion to <Terminal />. Shows the v2 adoption
// standard's per-CLI agentcookie.toml manifest, plus the on-disk
// surface where the synced KEY=VALUE secrets land on the sink. Text
// comes from lib/content/home.ts (MANIFEST, SINK_SURFACE).

import {
  MANIFEST,
  SINK_SURFACE,
  SECRETS_BUS_CAPTION,
  type ManifestTone,
} from "@/lib/content/home";

const TONE_CLASS: Record<ManifestTone, string | undefined> = {
  plain: undefined,
  muted: "text-text-2",
  string: "text-accent-sign",
  value: "text-accent-agent",
};

export function SecretsBusTile() {
  return (
    <div className="reveal-on-scroll flex flex-col">
      <div className="flex min-h-[360px] flex-col gap-4 rounded-lg border border-border-0 bg-bg-1 p-8 transition-colors hover:bg-bg-2">
        <div
          className="flex flex-col overflow-hidden rounded-md border border-border-0 bg-bg-0 font-display"
          style={{ fontSize: "13px", lineHeight: 1.7 }}
          aria-label={MANIFEST.label}
        >
          <div className="flex items-center gap-1.5 border-b border-border-0 bg-bg-1 px-3 py-2">
            <span
              className="inline-block h-2 w-2 rounded-full"
              style={{ background: "#404040" }}
            />
            <span
              className="inline-block h-2 w-2 rounded-full"
              style={{ background: "#2e2e2e" }}
            />
            <span
              className="inline-block h-2 w-2 rounded-full"
              style={{ background: "#2e2e2e" }}
            />
            <span className="ml-auto text-[11px] text-text-2">
              {MANIFEST.filename}
            </span>
          </div>
          <div className="flex-1 px-4 py-3.5">
            {MANIFEST.lines.map((line, i) => (
              <div
                key={i}
                className={line.gapAbove ? "mt-2 text-text-0" : "text-text-0"}
              >
                {line.tokens.map((token, j) =>
                  TONE_CLASS[token.tone] ? (
                    <span key={j} className={TONE_CLASS[token.tone]}>
                      {token.text}
                    </span>
                  ) : (
                    <React.Fragment key={j}>{token.text}</React.Fragment>
                  ),
                )}
              </div>
            ))}
          </div>
        </div>
        <div
          className="overflow-hidden rounded-md border border-border-0 bg-bg-0 px-4 py-3 font-display text-text-1"
          style={{ fontSize: "12px", lineHeight: 1.7 }}
        >
          <div className="text-text-2">{SINK_SURFACE.comment}</div>
          <div>
            <span className="text-text-2">$</span>{" "}
            <span className="text-text-0">{SINK_SURFACE.command}</span>
          </div>
          <div className="text-accent-agent">{SINK_SURFACE.output}</div>
        </div>
      </div>
      <p className="mx-2 mt-4 font-body text-sm text-text-1">
        {SECRETS_BUS_CAPTION}
      </p>
    </div>
  );
}

export default SecretsBusTile;
