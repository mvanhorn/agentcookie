// Canonical URLs the site links to. One home for them so the nav,
// the footer, and the Markdown twins never disagree about where the
// repo and its docs live.

export const GITHUB = "https://github.com/mvanhorn/agentcookie";

// Contact channels (product contract: GitHub issues and a DM on X,
// no email, no postal address). Kept outside LINKS because LINKS is
// the set every page must link to; these are for the trust pages.
export const ISSUES = `${GITHUB}/issues`;
export const MAINTAINER_X = "https://x.com/mvanhorn";
export const MAINTAINER_GITHUB = "https://github.com/mvanhorn";

// GitHub releases page: signed tarballs and checksums.
export const RELEASES = `${GITHUB}/releases`;

const BLOB = `${GITHUB}/blob/main`;

export const LINKS = {
  github: GITHUB,
  install: `${BLOB}/README.md#install`,
  quickstart: `${BLOB}/docs/quickstart.md`,
  secretsBusV1Spec: `${BLOB}/docs/spec-agentcookie-secrets-bus-v1.md`,
  v2AdoptionSpec: `${BLOB}/docs/spec-agentcookie-secrets-bus-v2-adoption.md`,
  threatModel: `${BLOB}/docs/threat-model.md`,
} as const;

export type LinkKey = keyof typeof LINKS;

export type NavLink = {
  label: string;
  href: (typeof LINKS)[LinkKey];
};

// Right-aligned nav slots, in order. The Install CTA is rendered
// separately by TopNav because it is a button, not a text link.
export const NAV_LINKS: readonly NavLink[] = [
  { label: "quickstart", href: LINKS.quickstart },
  { label: "spec", href: LINKS.v2AdoptionSpec },
  { label: "github", href: LINKS.github },
] as const;

// One row of repo + docs links in the footer, in order.
export const FOOTER_LINKS: readonly NavLink[] = [
  { label: "github.com/mvanhorn/agentcookie", href: LINKS.github },
  { label: "quickstart", href: LINKS.quickstart },
  { label: "secrets bus v1 spec", href: LINKS.secretsBusV1Spec },
  { label: "v2 adoption spec", href: LINKS.v2AdoptionSpec },
  { label: "threat model", href: LINKS.threatModel },
] as const;
