// Homepage FAQ - server component, no "use client". The copy must
// ship in the static HTML so an agent curling the page (and a
// reduced-JS visitor) sees the full answers.
//
// Leads with the DBSC question because it is the one a security-aware
// reader asks first: "doesn't Chrome's device-bound cookie protection
// break a tool that copies cookies between machines?" The answer
// mirrors docs/threat-model.md and the README DBSC section.

type FAQItem = {
  question: string;
  answer: string[];
};

const FAQS: FAQItem[] = [
  {
    question: "Does Chrome's device-bound cookie protection (DBSC) break agentcookie?",
    answer: [
      "No, not for the sites you use today. DBSC is opt-in per site: a cookie is device-bound only when the site's own backend asks for it. As of May 2026 the one broad adopter is Google's own account and Workspace cookies, generally available on Chrome for Windows first and rolling out on macOS in the next release. Almost every other site, and every Printing Press CLI agentcookie feeds, is unaffected and syncs as before.",
      "The secrets bus is untouched. DBSC is a cookie protocol, so bearer tokens, API keys, and OAuth refresh tokens that ride the bus replicate normally.",
      "For a site that has adopted DBSC, a copied cookie works on the second Mac only until its short-lived window of minutes lapses, because the second Mac cannot sign the refresh challenge held in the source Mac's Secure Enclave. agentcookie flags these in agentcookie doctor and ships them with a warning by default; pass --skip-dbsc-suspect to drop them instead. For Google sessions, sign the second Mac's Chrome into the same account once and it establishes its own device-bound session locally, no copy needed.",
    ],
  },
];

export function FAQ() {
  return (
    <section aria-label="frequently asked questions" className="pb-16">
      <h2 className="m-0 mb-8 font-display text-[28px] font-medium tracking-[-0.02em] text-text-0">
        frequently asked
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
