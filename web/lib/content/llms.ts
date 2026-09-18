// /llms.txt copy (R12). Guidance prose as plain data, plus the pure
// renderer that joins it with the route table and the links module.
// Every absolute URL comes from SITE_ORIGIN or the links module
// (R15); nothing here reads a request or an environment variable.
//
// Facts trace to README.md, docs/quickstart.md, and skill/SKILL.md.
// Command names and flags are copied from those files, not invented.

import { SITE_DESCRIPTION, SITE_NAME } from "./home";
import { GITHUB, ISSUES, LINKS, MAINTAINER_X, RELEASES } from "./links";
import { ROUTES } from "@/lib/routes";
import { SITE_ORIGIN } from "@/lib/site";

const BLOB = `${GITHUB}/blob/main`;
export const FAQ_URL = `${BLOB}/docs/faq.md`;
export const SKILL_URL = `${BLOB}/skill/SKILL.md`;
export const RELEASES_URL = RELEASES;
export const OPENAPI_URL = `${SITE_ORIGIN}/openapi.json`;

export const LLMS_INTRO: readonly string[] = [
  "agentcookie is a Go command-line tool. A Mac (the source) watches Chrome's cookie store and per-CLI secret files and pushes every change to one or more sinks (a Linux box or a second Mac) over the user's Tailscale tailnet. On Linux the sink injects cookies into a running Chrome through the DevTools protocol. MIT licensed.",
];

// Written as triggers: the situation an agent is in, or the words the
// user says, that mean agentcookie is the right tool. The quoted
// phrases are the install skill's trigger phrases (skill/SKILL.md).
export const LLMS_WHEN_TO_USE: readonly string[] = [
  "An agent on a Linux box or a second Mac needs the user's logged-in Chrome sessions and the user does not want to log in again on that machine.",
  "An agent needs per-CLI secrets (bearer tokens, API keys) that live on the user's laptop, without a paste-the-cookie or copy-the-token ritual.",
  'The user says "install agentcookie", "set up cookie sync", "share my Chrome sessions with my agent box", or "make my agent log in as me".',
  "Both machines are on the same Tailscale tailnet, the source is a Mac with Chrome, and the sink is Linux or macOS.",
];

export const LLMS_WHEN_NOT_TO_USE: readonly string[] = [
  "Google accounts and other sessions bound by Device Bound Session Credentials (DBSC): a replicated cookie works on the sink for only a few minutes. Sign the sink's Chrome into that account once instead.",
  "Sites that fingerprint the device and reject a session that moves to another host.",
  "Machines that are not on one Tailscale tailnet. agentcookie has no other transport.",
  "A Windows source or sink. The source is macOS only; the sink is Linux or macOS.",
  "Flows where a human has to click through a consent, CAPTCHA, or two-factor prompt on the sink. agentcookie moves existing sessions; it does not complete logins.",
];

export const LLMS_INSTALL_INTRO = `Release tarballs for darwin_arm64, linux_amd64, and linux_arm64, with a checksums.txt to verify against, are on the GitHub releases page: ${RELEASES_URL}. Or build from source:`;

export const LLMS_INSTALL_GO = "go install github.com/mvanhorn/agentcookie/cmd/agentcookie@latest";

// Pair, then sync. Commands and flags as printed by the tool and
// documented in docs/quickstart.md.
export const LLMS_INSTALL_STEPS: readonly string[] = [
  "On the source Mac, run `agentcookie pair --as source`. It prints a pairing code and a pair URL and waits for the sink.",
  "On the sink, run `agentcookie pair --as sink --peer <source-tailscale-hostname> --pair-url <pair-url> --code <pairing-code>`. Both sides print a matching fingerprint.",
  "On the sink, run `agentcookie sink`. On Linux, Chrome must already be running with `--remote-debugging-port=9223` so the sink can inject cookies.",
  "On the source Mac, run `agentcookie source --watch`. The first change pushes right away; later changes are batched at most once every 30 seconds.",
];

export const LLMS_INSTALL_OUTRO = `The sink's config files, the cookie allowlist and blocklist, and the daemon setup for launchd or systemd are in the quickstart and the install skill listed under Main pages. Run \`agentcookie doctor\` and \`agentcookie status --json\` to check a pairing.`;

export const LLMS_HOW_TO_READ: readonly string[] = [
  `Every page has a Markdown twin. Send \`Accept: text/markdown\` on a request to the HTML URL, or fetch the twin directly at ${SITE_ORIGIN}/md for the homepage and ${SITE_ORIGIN}/md/<path> for the others: ${SITE_ORIGIN}/md/about, ${SITE_ORIGIN}/md/contact, ${SITE_ORIGIN}/md/privacy, ${SITE_ORIGIN}/md/developers.`,
  "The HTML pages are static and complete without JavaScript; the hero copy, feature list, FAQ, and links are all in the served HTML.",
  `The homepage carries a JSON-LD graph (Organization, SoftwareApplication, WebSite) and a self-referencing canonical link. Every absolute URL on this site is on ${SITE_ORIGIN}.`,
  `This domain hosts no callable API, developer portal, or MCP server; there is nothing to call here beyond these pages, and every request under ${SITE_ORIGIN}/api answers a JSON 404. ${OPENAPI_URL} is an OpenAPI 3.1 description of the sink HTTP interface you run yourself on your own tailnet (GET /healthz and POST /sync on the sink, POST /pair on the source during pairing), and ${SITE_ORIGIN}/developers explains it. The pairing and sync URLs in the repository's quickstart and specs belong to that private protocol between two machines on one tailnet, not to a service on agentcookie.dev.`,
];

export const LLMS_CONTACT: readonly string[] = [
  `Bug reports, questions, and feature requests: ${ISSUES}`,
  `The maintainer on X: ${MAINTAINER_X}`,
  "There is no email address and no postal address.",
];

type Link = { label: string; href: string };

export function mainPages(): readonly Link[] {
  const pages: Link[] = ROUTES.map((route) => ({
    label: route.title,
    href: `${SITE_ORIGIN}${route.path}`,
  }));
  return [
    ...pages,
    { label: "OpenAPI description of the sink interface", href: OPENAPI_URL },
    { label: "Sitemap", href: `${SITE_ORIGIN}/sitemap.xml` },
    { label: "This file", href: `${SITE_ORIGIN}/llms.txt` },
    { label: "Source repository (GitHub)", href: GITHUB },
    { label: "Install section of the README", href: LINKS.install },
    { label: "FAQ", href: FAQ_URL },
    { label: "Install skill for agents (SKILL.md)", href: SKILL_URL },
    { label: "Releases", href: RELEASES_URL },
  ];
}

function bullets(items: readonly string[]): string {
  return items.map((item) => `- ${item}`).join("\n");
}

function numbered(items: readonly string[]): string {
  return items.map((item, i) => `${i + 1}. ${item}`).join("\n");
}

function links(items: readonly Link[]): string {
  return items.map((l) => `- [${l.label}](${l.href})`).join("\n");
}

export function renderLlmsTxt(): string {
  const parts = [
    `# ${SITE_NAME}`,
    `> ${SITE_DESCRIPTION}`,
    LLMS_INTRO.join("\n\n"),
    "## When to use agentcookie",
    bullets(LLMS_WHEN_TO_USE),
    "## When not to use agentcookie",
    bullets(LLMS_WHEN_NOT_TO_USE),
    "## How to install",
    LLMS_INSTALL_INTRO,
    `    ${LLMS_INSTALL_GO}`,
    "Pair, then sync:",
    numbered(LLMS_INSTALL_STEPS),
    LLMS_INSTALL_OUTRO,
    "## How to read this site",
    bullets(LLMS_HOW_TO_READ),
    "## Main pages",
    links(mainPages()),
    "## Contact",
    bullets(LLMS_CONTACT),
  ];
  return parts.join("\n\n") + "\n";
}
