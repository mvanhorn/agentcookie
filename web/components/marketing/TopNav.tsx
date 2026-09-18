// Global top navigation - server component.
//
// Right-aligned slot order: quickstart, spec, github, primary
// "Install" CTA that anchors back to the README install block.
// URLs come from lib/content/links.ts.

import Link from "next/link";
import { LINKS, NAV_LINKS } from "@/lib/content/links";
import { SITE_NAME } from "@/lib/content/home";

export function TopNav() {
  return (
    <nav
      aria-label="primary"
      className="flex h-16 items-center justify-between border-b border-border-0"
    >
      <Link
        href="/"
        className="font-display font-medium text-[18px] tracking-[-0.02em] text-text-0"
      >
        {SITE_NAME}
      </Link>
      <div className="flex items-center gap-6">
        {NAV_LINKS.map((link) => (
          <a
            key={link.href}
            href={link.href}
            className="font-body text-sm text-text-1 hover:text-text-0"
          >
            {link.label} <span className="font-display text-text-2">↗</span>
          </a>
        ))}
        <a
          href={LINKS.install}
          className="inline-flex items-center justify-center rounded-lg bg-accent-agent px-4 py-[9px] font-display text-sm font-medium tracking-[-0.01em] text-bg-0 transition-colors hover:bg-[#92f5ad] active:bg-[#6be089]"
        >
          Install
        </a>
      </div>
    </nav>
  );
}

export default TopNav;
