// Working-list feature grid. Three columns from md up, two from sm,
// one on mobile. Source of truth for entries is lib/features.ts so
// the README and the site stay in lockstep.

import { FEATURES } from "@/lib/features";
import { FeatureCard } from "./FeatureCard";

export function FeatureGrid() {
  return (
    <section
      aria-label="what's working today"
      className="pb-16"
    >
      <h2 className="m-0 mb-8 font-display text-[28px] font-medium tracking-[-0.02em] text-text-0">
        what&apos;s working today
      </h2>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 md:grid-cols-3">
        {FEATURES.map((feature) => (
          <FeatureCard key={feature.title} feature={feature} />
        ))}
      </div>
      <p className="mt-6 font-body text-sm text-text-2">
        macOS only on both ends today. 449+ unit tests across 26
        packages.
      </p>
    </section>
  );
}

export default FeatureGrid;
