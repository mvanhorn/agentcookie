// Trust page body - server component, synchronous.
//
// Renders one TrustPage from lib/content/trust.ts: the title as h1,
// the description as a lead line, each section as an h2 card of
// paragraphs, and the link list last. The Markdown twin mirrors the
// same order (title, description, h2 sections, links), so the two
// renderings of a page never drift in structure.
//
// Card, heading, and body classes follow FAQ.tsx. Prose is capped at
// 65ch so long paragraphs stay readable at the 1280px shell width.

import { TRUST_PAGES, type TrustKey } from "@/lib/content/trust";

export function TrustArticle({ pageKey }: { pageKey: TrustKey }) {
  const page = TRUST_PAGES[pageKey];
  return (
    <article
      aria-labelledby={`trust-${page.key}-title`}
      className="max-w-[65ch] py-16 pb-24"
    >
      <h1
        id={`trust-${page.key}-title`}
        className="m-0 mb-4 font-display text-[28px] font-medium tracking-[-0.02em] text-text-0"
      >
        {page.title}
      </h1>
      <p className="m-0 mb-10 font-body text-[16px] leading-relaxed text-text-1">
        {page.description}
      </p>
      <div className="grid grid-cols-1 gap-4">
        {page.sections.map((section) => (
          <section
            key={section.heading}
            id={section.id}
            className="rounded-lg border border-border-0 bg-bg-1 p-6"
          >
            <h2 className="m-0 mb-3 font-display text-[15px] font-medium tracking-[-0.01em] text-text-0">
              {section.heading}
            </h2>
            {section.paragraphs.map((paragraph, i) => (
              <p
                key={i}
                className="m-0 mb-3 font-body text-[14px] leading-relaxed text-text-1 last:mb-0"
              >
                {paragraph}
              </p>
            ))}
            {section.code ? (
              <pre className="m-0 mt-3 overflow-x-auto rounded-md border border-border-0 bg-bg-0 p-4 font-display text-[13px] leading-relaxed text-text-1">
                {section.code.join("\n")}
              </pre>
            ) : null}
          </section>
        ))}
      </div>
      <nav aria-label="related links" className="mt-10">
        <h2 className="m-0 mb-3 font-display text-[15px] font-medium tracking-[-0.01em] text-text-0">
          Links
        </h2>
        <ul className="m-0 flex list-none flex-col gap-2 p-0 font-body text-[14px] text-text-1">
          {page.links.map((link) => (
            <li key={link.href}>
              <a href={link.href} className="hover:text-text-0">
                {link.label}
              </a>
              <span className="ml-2 text-text-2">{link.href}</span>
            </li>
          ))}
        </ul>
      </nav>
    </article>
  );
}

export default TrustArticle;
