// Homepage Terminal tile - server component.
//
// Lifts the README's "What it looks like" triptych: instacart cart
// listing, ebay auction watch, table-reservation-goat omakase search.
// Each command runs on the second Mac over ssh; the cookies are
// already there because agentcookie shipped them from the laptop.
//
// Animation is pure CSS keyframes (see globals.css - `.terminal-line`
// and `.terminal-cursor`). Reduced motion disables the typing.

export function Terminal() {
  return (
    <div className="reveal-on-scroll flex flex-col">
      <div className="flex min-h-[360px] flex-col rounded-lg border border-border-0 bg-bg-1 p-8 transition-colors hover:bg-bg-2">
        <div
          className="flex flex-1 flex-col overflow-hidden rounded-md border border-border-0 bg-bg-0 font-display"
          style={{ fontSize: "13px", lineHeight: 1.7 }}
          aria-label="agentcookie second-Mac session"
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
              you@laptop:~
            </span>
          </div>
          <div className="flex-1 px-4 py-3.5">
            <div className="terminal-line t-l1 flex items-baseline gap-2 overflow-hidden whitespace-nowrap">
              <span className="text-text-2">$</span>
              <span className="text-text-0">
                ssh second-mac &apos;instacart-pp-cli carts&apos;
              </span>
            </div>
            <div className="terminal-line t-l2 flex items-baseline gap-2 overflow-hidden whitespace-nowrap">
              <span className="text-text-1">
                Costco · slug=costco · cart=757109404 · 5 items
              </span>
            </div>
            <div className="terminal-line t-l3 flex items-baseline gap-2 overflow-hidden whitespace-nowrap">
              <span className="text-text-1">
                Safeway · slug=safeway · cart=3190 · 1 item
              </span>
            </div>
            <div className="terminal-line t-l4 flex items-baseline gap-2 overflow-hidden whitespace-nowrap">
              <span className="text-text-2">$</span>
              <span className="text-text-0">
                ssh second-mac &apos;ebay-pp-cli auctions watch
                --ending-within 1h&apos;
              </span>
            </div>
            <div className="terminal-line t-l5 flex items-baseline gap-2 overflow-hidden whitespace-nowrap">
              <span className="text-text-1">
                $352 · 23 bids · 1m left · Apple Watch Ultra 2 49mm
              </span>
            </div>
            <div className="terminal-line t-l6 flex items-baseline gap-2 overflow-hidden whitespace-nowrap">
              <span className="text-text-2">$</span>
              <span className="terminal-cursor text-text-0">
                ssh second-mac &apos;table-reservation-goat goat
                &quot;omakase&quot;&apos;
              </span>
            </div>
            <div className="terminal-line t-l7 flex items-baseline gap-2 overflow-hidden whitespace-nowrap">
              <span className="text-accent-agent">
                ✓ 12 results · OpenTable + Tock · already signed in
              </span>
            </div>
          </div>
        </div>
      </div>
      <p
        data-bento-caption="terminal"
        className="mx-2 mt-4 font-body text-sm text-text-1"
      >
        no <code className="font-display text-text-0">auth login</code>,
        no Keychain prompt, no paste-the-cookie ritual. cookies were
        already there.
      </p>
    </div>
  );
}

export default Terminal;
