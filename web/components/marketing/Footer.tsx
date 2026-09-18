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
import { ROUTES } from "@/lib/routes";

const TRUST_ROUTES = ROUTES.filter((route) => route.path !== "/");

export function Footer() {
  return (
    <footer className="flex flex-col gap-3 border-t border-border-0 py-6 pb-12">
      <div className="flex flex-wrap items-center gap-3 text-[13px] text-text-1">
        {FOOTER_LINKS.map((link, i) => (
          <React.Fragment key={link.href}>
            {i > 0 ? <span className="text-text-2">·</span> : null}
            <a href={link.href} className="hover:text-text-0">
              {link.label}
            </a>
          </React.Fragment>
        ))}
      </div>
      <div className="flex flex-wrap items-center gap-3 text-[13px] text-text-1">
        {TRUST_ROUTES.map((route, i) => (
          <React.Fragment key={route.path}>
            {i > 0 ? <span className="text-text-2">·</span> : null}
            <a href={route.path} className="hover:text-text-0">
              {route.path.slice(1)}
            </a>
          </React.Fragment>
        ))}
      </div>
      <div className="text-[13px] text-text-2">{FOOTER_LINE}</div>
    </footer>
  );
}

export default Footer;
