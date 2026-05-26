// Homepage SecretsBusTile - server component.
//
// Right-hand companion to <Terminal />. Shows the v0.14 adoption
// standard's per-CLI agentcookie.toml manifest, plus the on-disk
// surface where the synced KEY=VALUE secrets land on the sink.

export function SecretsBusTile() {
  return (
    <div className="reveal-on-scroll flex flex-col">
      <div className="flex min-h-[360px] flex-col gap-4 rounded-lg border border-border-0 bg-bg-1 p-8 transition-colors hover:bg-bg-2">
        <div
          className="flex flex-col overflow-hidden rounded-md border border-border-0 bg-bg-0 font-display"
          style={{ fontSize: "13px", lineHeight: 1.7 }}
          aria-label="agentcookie.toml adoption manifest"
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
              stripe-pp-cli/agentcookie.toml
            </span>
          </div>
          <div className="flex-1 px-4 py-3.5">
            <div className="text-text-2"># adoption manifest v2</div>
            <div className="text-text-0">
              schema_version <span className="text-text-2">=</span>{" "}
              <span className="text-accent-agent">2</span>
            </div>
            <div className="text-text-0">
              name <span className="text-text-2">=</span>{" "}
              <span className="text-accent-sign">
                &quot;stripe-pp-cli&quot;
              </span>
            </div>
            <div className="text-text-0">
              display_name <span className="text-text-2">=</span>{" "}
              <span className="text-accent-sign">&quot;Stripe&quot;</span>
            </div>
            <div className="mt-2 text-text-0">
              <span className="text-text-2">[</span>secrets.file
              <span className="text-text-2">]</span>
            </div>
            <div className="text-text-0">
              path <span className="text-text-2">=</span>{" "}
              <span className="text-accent-sign">
                &quot;~/.config/stripe-pp-cli/config.toml&quot;
              </span>
            </div>
            <div className="mt-2 text-text-0">
              <span className="text-text-2">[</span>sync.keys
              <span className="text-text-2">]</span>
            </div>
            <div className="text-text-0">
              STRIPE_SECRET_KEY <span className="text-text-2">=</span>{" "}
              <span className="text-accent-agent">true</span>
            </div>
          </div>
        </div>
        <div
          className="overflow-hidden rounded-md border border-border-0 bg-bg-0 px-4 py-3 font-display text-text-1"
          style={{ fontSize: "12px", lineHeight: 1.7 }}
        >
          <div className="text-text-2">
            # arrives on the sink at mode 0600
          </div>
          <div>
            <span className="text-text-2">$</span>{" "}
            <span className="text-text-0">
              cat ~/.agentcookie/secrets/stripe-pp-cli/secrets.env
            </span>
          </div>
          <div className="text-accent-agent">
            STRIPE_SECRET_KEY=sk_live_...
          </div>
        </div>
      </div>
      <p className="mx-2 mt-4 font-body text-sm text-text-1">
        per-CLI bearer tokens and API keys, declared once, synced
        across the wire. read by every PP CLI; readable by 1Password
        or whatever else fills the bus.
      </p>
    </div>
  );
}

export default SecretsBusTile;
