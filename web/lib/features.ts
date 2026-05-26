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
    title: "three cookie delivery surfaces",
    body: "Chrome's SQLite re-encrypted for the sink keychain, plaintext sidecar at ~/.agentcookie/cookies-plain.db, or per-CLI adapter session files.",
  },
  {
    title: "zero-config drop-in for five PP CLIs",
    body: "instacart, airbnb, ebay, pagliacci, table-reservation-goat. anything else reads the universal surfaces above.",
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
    body: "every release binary signed and timestamped. per-binary -T Keychain ACL on Chrome Safe Storage so no AllowAlways prompt fires after install.",
  },
  {
    title: "headless install over SSH",
    body: "no GUI clicks required. install-beta.sh runs end to end on a Mac mini you have never opened a window on.",
  },
  {
    title: "11-category doctor",
    body: "binary signature, Tailscale, config, keystore, listener bind, sink/source state, sealing posture, adapter coverage, CDP injector health, and secrets bus coverage.",
  },
];
