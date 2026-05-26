// Global top navigation - server component.
//
// Right-aligned slot order: quickstart, spec, github, primary
// "Install" CTA that anchors back to the README install block.

import Link from "next/link";

const GITHUB = "https://github.com/mvanhorn/agentcookie";
const README = `${GITHUB}/blob/main/README.md`;
const QUICKSTART = `${GITHUB}/blob/main/docs/quickstart.md`;
const SPEC =
  `${GITHUB}/blob/main/docs/spec-agentcookie-secrets-bus-v2-adoption.md`;

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
        agentcookie
      </Link>
      <div className="flex items-center gap-6">
        <a
          href={QUICKSTART}
          className="font-body text-sm text-text-1 hover:text-text-0"
        >
          quickstart <span className="font-display text-text-2">↗</span>
        </a>
        <a
          href={SPEC}
          className="font-body text-sm text-text-1 hover:text-text-0"
        >
          spec <span className="font-display text-text-2">↗</span>
        </a>
        <a
          href={GITHUB}
          className="font-body text-sm text-text-1 hover:text-text-0"
        >
          github <span className="font-display text-text-2">↗</span>
        </a>
        <a
          href={`${README}#install`}
          className="inline-flex items-center justify-center rounded-lg bg-accent-agent px-4 py-[9px] font-display text-sm font-medium tracking-[-0.01em] text-bg-0 transition-colors hover:bg-[#92f5ad] active:bg-[#6be089]"
        >
          Install
        </a>
      </div>
    </nav>
  );
}

export default TopNav;
