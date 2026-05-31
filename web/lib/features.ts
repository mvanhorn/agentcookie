// Source of truth for FeatureGrid. Each entry maps to one Working
// bullet in the agentcookie README. Adding a card here is the same
// as adding a bullet to the README - when the README is rewritten,
// the page.test.tsx assertions on this list catch drift.

export type Feature = {
  title: string;
  body: string;
};

export const FEATURES: Feature[] = [
  {
    title: "continuous laptop -> sink sync",
    body: "fsnotify on Chrome's Cookies file, debounced, allowlist + blocklist filtered, AES-256-GCM over Tailscale.",
  },
  {
    title: "universal cookie delivery",
    body: "one login-password entry at install (no GUI click) opens Chrome Safe Storage to any cookie reader, so unmodified tools - yt-dlp, gallery-dl, browser-driving agents, the Printing Press CLIs - read the real synced Default Chrome profile. verified live on macOS 15.x.",
  },
  {
    title: "three cookie delivery surfaces",
    body: "universal (the real Default profile + one-password keychain open) is the default; the plaintext sidecar at ~/.agentcookie/cookies-plain.db and per-CLI adapter session files are the agentcookie-aware paths that also work in degraded mode.",
  },
  {
    title: "works with Printing Press CLIs like",
    body: "Stripe, Linear, Notion, Granola, Slack, Kalshi, ElevenLabs, Mercury, and dozens more - anything with a bearer token or API key reads the secrets bus. Five (instacart, airbnb, ebay, pagliacci, table-reservation-goat) additionally get a bespoke cookie adapter.",
  },
  {
    title: "per-CLI secrets bus",
    body: "bearer tokens, API keys, KEY=VALUE auth blobs ride the same encrypted push and land at ~/.agentcookie/secrets/<cli>/secrets.env (mode 0600) with an optional sealed twin.",
  },
  {
    title: "v2 adoption standard",
    body: "drop an agentcookie.toml in your repo and agentcookie discover auto-detects it. three integration tiers (explicit, pp-cli-derived, legacy v1) coexist.",
  },
  {
    title: "tailnet-only listeners",
    body: "both ends bind tailnet-private addresses. pair endpoint is rate-limited with a 64-bit code.",
  },
  {
    title: "replay defense, per-peer keys",
    body: "persistent replay defense and pairing-derived per-peer keys; pairing-code rotation re-derives both ends.",
  },
  {
    title: "Apple Developer ID signed",
    body: "every release binary signed and timestamped. the sink daemon reads Chrome Safe Storage via the teamid: partition - no per-binary trust list, no recreate of the key value, no AllowAlways prompt after install.",
  },
  {
    title: "headless install over SSH",
    body: "one login-password entry, no GUI SecurityAgent click. a box with no password lands in degraded mode (sidecar + adapters) and prints the one-line upgrade command.",
  },
  {
    title: "fifteen-category doctor",
    body: "cookie delivery (universal vs degraded, with duplicate-keychain-item race detection), binary signature + install, Tailscale, config, keystore, listener bind, sink/source state, sealing posture, adapter coverage, CDP injector health, secrets-bus + secret coverage, and DBSC-suspect cookies.",
  },
];
