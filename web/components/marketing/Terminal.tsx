// Homepage Terminal tile - server component.
//
// Lifts the README's "What it looks like" triptych: instacart cart
// listing, ebay auction watch, table-reservation-goat omakase search.
// Each command runs on the sink over ssh; the cookies are already
// there because agentcookie shipped them from the laptop. Lines come
// from lib/content/home.ts (TERMINAL).
//
// Animation is pure CSS keyframes (see globals.css - `.terminal-line`
// and `.terminal-cursor`). Reduced motion disables the typing.

import { TERMINAL } from "@/lib/content/home";

const LINE_CLASS =
  "terminal-line flex items-baseline gap-2 overflow-hidden whitespace-nowrap";

export function Terminal() {
  return (
    <div className="reveal-on-scroll flex flex-col">
      <div className="flex min-h-[360px] flex-col rounded-lg border border-border-0 bg-bg-1 p-8 transition-colors hover:bg-bg-2">
        <div
          className="flex flex-1 flex-col overflow-hidden rounded-md border border-border-0 bg-bg-0 font-display"
          style={{ fontSize: "13px", lineHeight: 1.7 }}
          aria-label={TERMINAL.label}
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
              {TERMINAL.prompt}
            </span>
          </div>
          <div className="flex-1 px-4 py-3.5">
            {TERMINAL.lines.map((line, i) => (
              <div key={i} className={`${LINE_CLASS} t-l${i + 1}`}>
                {line.kind === "command" ? (
                  <>
                    <span className="text-text-2">$</span>
                    <span
                      className={
                        line.cursor
                          ? "terminal-cursor text-text-0"
                          : "text-text-0"
                      }
                    >
                      {line.text}
                    </span>
                  </>
                ) : (
                  <span
                    className={
                      line.tone === "success"
                        ? "text-accent-agent"
                        : "text-text-1"
                    }
                  >
                    {line.text}
                  </span>
                )}
              </div>
            ))}
          </div>
        </div>
      </div>
      <p
        data-bento-caption="terminal"
        className="mx-2 mt-4 font-body text-sm text-text-1"
      >
        {TERMINAL.caption.before}
        <code className="font-display text-text-0">
          {TERMINAL.caption.code}
        </code>
        {TERMINAL.caption.after}
      </p>
    </div>
  );
}

export default Terminal;
