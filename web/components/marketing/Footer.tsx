// Homepage footer - server component.
//
// One row of repo + docs links. The threat-model link is the
// signal that this is a serious-security project, not a casual
// cookie syncer. URLs come from lib/content/links.ts.

import React from "react";
import { FOOTER_LINKS } from "@/lib/content/links";
import { FOOTER_LINE } from "@/lib/content/home";

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
      <div className="text-[13px] text-text-2">{FOOTER_LINE}</div>
    </footer>
  );
}

export default Footer;
