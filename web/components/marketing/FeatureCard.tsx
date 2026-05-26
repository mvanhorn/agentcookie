// Presentational tile for the FeatureGrid. Title in Geist Mono,
// body in Geist Sans.

import type { Feature } from "@/lib/features";

export function FeatureCard({ feature }: { feature: Feature }) {
  return (
    <div className="reveal-on-scroll rounded-lg border border-border-0 bg-bg-1 p-6 transition-colors hover:bg-bg-2">
      <h3 className="m-0 mb-2 font-display text-[15px] font-medium tracking-[-0.01em] text-text-0">
        {feature.title}
      </h3>
      <p className="m-0 font-body text-[14px] leading-relaxed text-text-1">
        {feature.body}
      </p>
    </div>
  );
}

export default FeatureCard;
