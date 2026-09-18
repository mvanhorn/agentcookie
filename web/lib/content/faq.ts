// Homepage FAQ copy. Lives here (not in the component) so the
// Markdown twin and the JSX render the same questions and answers.
//
// Leads with the DBSC question because it is the one a security-aware
// reader asks first: "doesn't Chrome's device-bound cookie protection
// break a tool that copies cookies between machines?" The answer
// mirrors docs/threat-model.md and the README DBSC section.

export type FAQItem = {
  question: string;
  answer: readonly string[];
};

export const FAQ_HEADING = "frequently asked";

export const FAQS: readonly FAQItem[] = [
  {
    question:
      "Does Chrome's device-bound cookie protection (DBSC) break agentcookie?",
    answer: [
      "No, not for the sites you use today. DBSC is opt-in per site: a cookie is device-bound only when the site's own backend asks for it. As of August 2026 the one broad adopter is Google's own account and Workspace cookies. Almost every other site, and every Printing Press CLI agentcookie feeds, is unaffected and syncs as before.",
      "The secrets bus is untouched. DBSC is a cookie protocol, so bearer tokens, API keys, and OAuth refresh tokens that ride the bus replicate normally.",
      "For a site that has adopted DBSC, a copied cookie works on the sink only until its short-lived window of minutes lapses, because the sink cannot sign the refresh challenge held in the source Mac's Secure Enclave. agentcookie flags these in agentcookie doctor and ships them with a warning by default; pass --skip-dbsc-suspect to drop them instead. For Google sessions, sign the sink's Chrome into the same account once and it establishes its own device-bound session locally, no copy needed.",
    ],
  },
] as const;
