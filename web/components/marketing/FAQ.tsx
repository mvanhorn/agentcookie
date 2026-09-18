// Homepage FAQ - server component, no "use client". The copy must
// ship in the static HTML so an agent curling the page (and a
// reduced-JS visitor) sees the full answers. Questions and answers
// live in lib/content/faq.ts.

import { FAQS, FAQ_HEADING } from "@/lib/content/faq";

export function FAQ() {
  return (
    <section aria-label="frequently asked questions" className="pb-16">
      <h2 className="m-0 mb-8 font-display text-[28px] font-medium tracking-[-0.02em] text-text-0">
        {FAQ_HEADING}
      </h2>
      <div className="grid grid-cols-1 gap-4">
        {FAQS.map((faq) => (
          <div
            key={faq.question}
            className="reveal-on-scroll rounded-lg border border-border-0 bg-bg-1 p-6"
          >
            <h3 className="m-0 mb-3 font-display text-[15px] font-medium tracking-[-0.01em] text-text-0">
              {faq.question}
            </h3>
            {faq.answer.map((para, i) => (
              <p
                key={i}
                className="m-0 mb-3 font-body text-[14px] leading-relaxed text-text-1 last:mb-0"
              >
                {para}
              </p>
            ))}
          </div>
        ))}
      </div>
    </section>
  );
}

export default FAQ;
