// Trust page copy: about, contact, privacy, plus the types every such
// page shares (developers lives in developers.ts and is registered
// here). Plain data, rendered by the HTML pages and re-emitted by the
// Markdown twins. Facts trace to the root README.md and
// docs/threat-model.md; contact channels are GitHub issues and a DM
// on x.com/mvanhorn only (no email, no postal address). Nothing here
// carries a test count, so the README's number is stated in exactly
// one place (home.ts).

import {
  LINKS,
  ISSUES,
  MAINTAINER_X,
  MAINTAINER_GITHUB,
} from "./links";
import { DEVELOPERS } from "./developers";

export type TrustKey = "about" | "contact" | "privacy" | "developers";

export type TrustSection = {
  heading: string;
  paragraphs: readonly string[];
  // Optional fragment id, so an error `type` URI such as
  // /developers#error-not-found lands on the section that explains it.
  id?: string;
  // Optional verbatim lines rendered after the paragraphs as one
  // code block (a <pre> in HTML, a fence in the Markdown twin).
  code?: readonly string[];
};

export type TrustLink = {
  label: string;
  href: string;
};

export type TrustPage = {
  key: TrustKey;
  title: string;
  // One-sentence summary for <meta name="description"> and the twin's
  // lead line.
  description: string;
  sections: readonly TrustSection[];
  // Rendered as a link list after the sections.
  links: readonly TrustLink[];
};

export const ABOUT: TrustPage = {
  key: "about",
  title: "About agentcookie",
  description:
    "What agentcookie does, who maintains it, and where the code and docs live.",
  sections: [
    {
      heading: "What it does",
      paragraphs: [
        "agentcookie is one-way, continuous, unattended replication of Chrome cookies and per-CLI secrets from the Mac you use to the Linux box or second Mac your agents run on. You browse and log in normally on your Mac. agentcookie watches Chrome's Cookies file and ships the diff to the sink the moment anything changes, so the agent's session is already there when its request hits. There is no auth login step on the sink, no Keychain prompt, and no paste-the-cookie ritual.",
        "Everything travels encrypted over your own Tailscale tailnet. Both ends bind tailnet-private addresses only. Keys are derived per peer at pairing time (X25519 plus HKDF salted with the pairing code), every payload is sealed with AES-256-GCM, and a persistent sequence tracker rejects replays across restarts. Cookie policy filters run on both sides: the source decides what to ship and the sink independently decides what to accept, so a compromised source cannot push a domain the sink has not allowed.",
        "On a Linux sink there is no Keychain and no Chrome SQLite rewrite. The sink attaches to Chrome's debug port and performs live CDP injection straight into the in-memory cookie store, on every sync and on every new browser context. browserUse, Puppeteer, Playwright, or any Chromium automation on that box sees the session already present. A macOS sink additionally opens Chrome Safe Storage to any cookie reader with one login-password entry at install, so unmodified tools read the real synced profile.",
        "One source fans out to several sinks. Each sink is sealed with its own paired key, and a sink that is down fails on its own while the others still receive the payload. Alongside cookies, a per-CLI secrets bus carries bearer tokens, API keys, and KEY=VALUE auth blobs over the same encrypted push; they land on the sink at ~/.agentcookie/secrets/<cli>/secrets.env with mode 0600. Any tool can adopt the bus by dropping an agentcookie.toml manifest in its repo.",
      ],
    },
    {
      heading: "Who maintains it",
      paragraphs: [
        "agentcookie is written and maintained by Matt Van Horn (x.com/mvanhorn, GitHub mvanhorn). It is a single-maintainer project. Development happens in the open on GitHub, releases are signed with an Apple Developer ID and published with checksums on the GitHub Releases page, and the threat model is a tracked document in the same repository.",
      ],
    },
    {
      heading: "License",
      paragraphs: [
        "agentcookie is open source under the MIT license. The full source, the quickstart, the secrets bus specifications, and the threat model are all in the repository linked below. If the site and the README ever disagree, the README is the source of truth.",
      ],
    },
  ],
  links: [
    { label: "Repository", href: LINKS.github },
    { label: "Quickstart", href: LINKS.quickstart },
    { label: "Secrets bus v1 spec", href: LINKS.secretsBusV1Spec },
    { label: "v2 adoption spec", href: LINKS.v2AdoptionSpec },
    { label: "Threat model", href: LINKS.threatModel },
    { label: "Matt Van Horn on X", href: MAINTAINER_X },
    { label: "Matt Van Horn on GitHub", href: MAINTAINER_GITHUB },
  ],
};

export const CONTACT: TrustPage = {
  key: "contact",
  title: "Contact",
  description:
    "How to reach the agentcookie maintainer: GitHub issues for bugs and questions, a DM on X for sensitive security findings.",
  sections: [
    {
      heading: "Bugs, feature requests, and questions",
      paragraphs: [
        `Open an issue at ${ISSUES}. That is the right channel for bugs, feature requests, questions about setup or the threat model, and anything else that does not need to stay private. Issues are public, searchable, and the place where fixes get tracked, so please check the existing ones before filing a new one.`,
      ],
    },
    {
      heading: "Sensitive security findings",
      paragraphs: [
        `If you have found something that should not be disclosed publicly before it is fixed, send a direct message to the maintainer on X at ${MAINTAINER_X}. Please do not put exploit details in a public issue. Once a fix has shipped, a public issue or a note in the release is welcome.`,
      ],
    },
    {
      heading: "What to include in a report",
      paragraphs: [
        "The agentcookie version on each machine (agentcookie --version), the operating system and version on the source and on each sink, and the redacted output of agentcookie doctor from both sides. Doctor output is designed to be safe to share, but read it before you paste it. Never include cookie values, secrets, pairing codes, or the contents of ~/.config/agentcookie in a report; describe the shape of the problem instead.",
      ],
    },
    {
      heading: "What to expect",
      paragraphs: [
        "agentcookie is maintained by one person, so responses are best-effort and can take a few days. There is no email address published for this project and no phone number; the two channels above are the only ones. Automated outreach, recruiter mail, and link-exchange requests will not get a reply.",
      ],
    },
  ],
  links: [
    { label: "GitHub issues", href: ISSUES },
    { label: "Matt Van Horn on X", href: MAINTAINER_X },
    { label: "Threat model", href: LINKS.threatModel },
  ],
};

export const PRIVACY: TrustPage = {
  key: "privacy",
  title: "Privacy",
  description:
    "What this website records about visitors (almost nothing) and where the agentcookie tool sends your data (nowhere outside your own tailnet).",
  sections: [
    {
      heading: "This website",
      paragraphs: [
        "agentcookie.dev sets no cookies. It runs no analytics, no tracking pixels, and no third-party scripts. The fonts are self-hosted with the site rather than loaded from a font service, so rendering a page makes requests to this origin only. There are no forms, no sign-ups, no comments, and nothing that identifies a visitor, so the site stores nothing about you.",
        "The site is hosted on Vercel. Vercel keeps standard request logs for the deployment, which include the requested URL and query string, the response status, and the connection metadata a web server sees. Those request logs are the only server-side data that exists about a visit; the site adds nothing to them and does not export them anywhere. Refer to Vercel's own privacy documentation for how long they retain logs.",
      ],
    },
    {
      heading: "The agentcookie tool",
      paragraphs: [
        "The tool moves cookies and secrets only between machines you own and paired yourself, inside your own Tailscale tailnet. There is no hosted relay, no cloud account, and no telemetry: agentcookie never contacts agentcookie.dev or any other server of ours, and the maintainer has no way to see what you sync. Both listeners refuse to bind anything but a tailnet-private address.",
        "Cookie values never appear in agentcookie's logs or in agentcookie doctor output. The pairing-derived keys live in your own configuration directory under ~/.config/agentcookie, and the sink's on-disk copies can be sealed under a key in your own Keychain. Anyone with access to those files has whatever access the files grant; that is the trust boundary, and it is yours.",
        "The threat model describes exactly what agentcookie does and does not protect against, including root on either machine, a compromised Chrome, and device-bound session credentials. Read it before deploying anywhere you care about.",
      ],
    },
  ],
  links: [
    { label: "Threat model", href: LINKS.threatModel },
    { label: "Repository", href: LINKS.github },
  ],
};

export const TRUST_PAGES: Readonly<Record<TrustKey, TrustPage>> = {
  about: ABOUT,
  contact: CONTACT,
  privacy: PRIVACY,
  developers: DEVELOPERS,
};

// Every paragraph of a page joined into one string. The length guard
// in trust.test.ts (R10: at least 500 characters) and the Markdown
// twin's word count read this.
export function trustBodyText(page: TrustPage): string {
  return page.sections
    .flatMap((section) => section.paragraphs)
    .join("\n\n");
}
