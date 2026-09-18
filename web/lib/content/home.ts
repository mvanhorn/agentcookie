// Homepage copy. Every string the marketing components render (and
// the Markdown twin re-emits) lives here as plain data. Components
// keep their per-token styling and read text from these objects.
//
// Facts trace to the root README.md: a Mac is the source; the sink is
// a Linux box (live CDP injection into Chrome) or a second Mac; one
// source fans out to several sinks; the test count is the README's.

export const SITE_NAME = "agentcookie";

export const SITE_TITLE =
  "agentcookie - session state sync for the agent on your Linux box or second Mac";

export const SITE_DESCRIPTION =
  "Cookies and per-CLI secrets, replicated continuously from your laptop to the Linux box or second Mac your agent runs on. Encrypted over Tailscale, zero per-site auth ceremony.";

// Hero. The tagline's "replicated continuously from your laptop" and
// "Tailscale" fragments are asserted by app/(marketing)/page.test.tsx.
export const HERO = {
  headline: ["your agent's", "session state, synced"],
  tagline:
    "cookies, bearer tokens, and per-CLI auth blobs replicated continuously from your laptop to the Linux box or second Mac your agent runs on. encrypted over Tailscale, zero per-site auth ceremony.",
} as const;

// Terminal tile. Each line is typed so the component can style the
// prompt, the command, and the output separately. `t-l<n>` keyframe
// classes in globals.css exist for lines 1 through 7.
export type TerminalLine =
  | { kind: "command"; text: string; cursor?: boolean }
  | { kind: "output"; text: string; tone: "muted" | "success" };

export const TERMINAL = {
  label: "agentcookie sink session",
  prompt: "you@laptop:~",
  lines: [
    { kind: "command", text: "ssh sink 'instacart-pp-cli carts'" },
    {
      kind: "output",
      tone: "muted",
      text: "Costco · slug=costco · cart=757109404 · 5 items",
    },
    {
      kind: "output",
      tone: "muted",
      text: "Safeway · slug=safeway · cart=3190 · 1 item",
    },
    {
      kind: "command",
      text: "ssh sink 'ebay-pp-cli auctions watch --ending-within 1h'",
    },
    {
      kind: "output",
      tone: "muted",
      text: "$352 · 23 bids · 1m left · Apple Watch Ultra 2 49mm",
    },
    {
      kind: "command",
      text: "ssh sink 'table-reservation-goat goat \"omakase\"'",
      cursor: true,
    },
    {
      kind: "output",
      tone: "success",
      text: "✓ 12 results · OpenTable + Tock · already signed in",
    },
  ] satisfies readonly TerminalLine[],
  // Caption under the tile; `code` renders in the display font.
  caption: {
    before: "no ",
    code: "auth login",
    after:
      ", no Keychain prompt, no paste-the-cookie ritual. cookies were already there.",
  },
} as const;

export const TERMINAL_COMMANDS: readonly string[] = TERMINAL.lines
  .filter((line) => line.kind === "command")
  .map((line) => line.text);

// Secrets bus tile: the v2 adoption manifest and the on-disk surface
// where synced KEY=VALUE secrets land on the sink.
export type ManifestTone = "plain" | "muted" | "string" | "value";

export type ManifestToken = { text: string; tone: ManifestTone };

export type ManifestLine = {
  tokens: readonly ManifestToken[];
  gapAbove?: boolean;
};

const plain = (text: string): ManifestToken => ({ text, tone: "plain" });
const muted = (text: string): ManifestToken => ({ text, tone: "muted" });
const str = (text: string): ManifestToken => ({ text, tone: "string" });
const value = (text: string): ManifestToken => ({ text, tone: "value" });

export const MANIFEST = {
  label: "agentcookie.toml adoption manifest",
  filename: "stripe-pp-cli/agentcookie.toml",
  lines: [
    { tokens: [muted("# adoption manifest v2")] },
    { tokens: [plain("schema_version "), muted("="), plain(" "), value("2")] },
    {
      tokens: [plain("name "), muted("="), plain(" "), str('"stripe-pp-cli"')],
    },
    {
      tokens: [plain("display_name "), muted("="), plain(" "), str('"Stripe"')],
    },
    {
      gapAbove: true,
      tokens: [muted("["), plain("secrets.file"), muted("]")],
    },
    {
      tokens: [
        plain("path "),
        muted("="),
        plain(" "),
        str('"~/.config/stripe-pp-cli/config.toml"'),
      ],
    },
    { gapAbove: true, tokens: [muted("["), plain("sync.keys"), muted("]")] },
    {
      tokens: [
        plain("STRIPE_SECRET_KEY "),
        muted("="),
        plain(" "),
        value("true"),
      ],
    },
  ] satisfies readonly ManifestLine[],
} as const;

export const SINK_SURFACE = {
  comment: "# arrives on the sink at mode 0600",
  command: "cat ~/.agentcookie/secrets/stripe-pp-cli/secrets.env",
  output: "STRIPE_SECRET_KEY=sk_live_...",
} as const;

export const SECRETS_BUS_CAPTION =
  "per-CLI bearer tokens and API keys, declared once, synced across the wire. read by every PP CLI; readable by 1Password or whatever else fills the bus.";

export const WHAT_IT_SYNCS = {
  label: "what agentcookie syncs",
  caption:
    "two surfaces, one encrypted push. cookies for browser-driving agents and adapter-equipped CLIs; secrets bus for everything with bearer auth.",
} as const;

export const FEATURE_GRID = {
  heading: "what's working today",
  trailer:
    "the source is a Mac. the sink is a Linux box (live CDP injection into Chrome's in-memory store) or a second Mac, and one source fans out to several sinks. 520+ unit tests across 26 packages.",
} as const;

export const FOOTER_LINE = "MIT licensed. Mac source; Linux or Mac sink.";
