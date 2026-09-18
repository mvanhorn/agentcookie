// Site footer - server component.
//
// One row of repo + docs links, then one row of the site's own trust
// pages read from the static route table, so a new page in
// lib/routes.ts appears here without a footer edit. The threat-model
// link is the signal that this is a serious-security project, not a
// casual cookie syncer. External URLs come from lib/content/links.ts.
// Internal links are plain anchors (KTD5): typed routes regenerate
// only on build, and the route table is the source of truth.

import React from "react";
import { FOOTER_LINKS } from "@/lib/content/links";
import { FOOTER_LINE } from "@/lib/content/home";
import { TRUST_ROUTE_LINKS } from "@/lib/routes";

const TRUST_LINKS = TRUST_ROUTE_LINKS;

function LinkRow({ items }: { items: readonly { href: string; label: string }[] }) {
  return (
    <div className="flex flex-wrap items-center gap-3 text-[13px] text-text-1">
      {items.map((link, i) => (
        <React.Fragment key={link.href}>
          {i > 0 ? <span className="text-text-2">·</span> : null}
          <a href={link.href} className="hover:text-text-0">
            {link.label}
          </a>
        </React.Fragment>
      ))}
    </div>
  );
}

export function Footer() {
  return (
    <footer className="flex flex-col gap-3 border-t border-border-0 py-6 pb-12">
      <LinkRow items={FOOTER_LINKS} />
      <LinkRow items={TRUST_LINKS} />
      <div className="text-[13px] text-text-2">{FOOTER_LINE}</div>
    </footer>
  );
}

export default Footer;
