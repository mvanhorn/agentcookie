// Homepage WhatItSyncs - equal-weight two-tile grid.
//
// "What gets synced": cookies (Terminal demo of CLIs reading them on
// the sink) and per-CLI secrets (the v2 adoption manifest + on-disk
// surface). Caption text comes from lib/content/home.ts.

import { WHAT_IT_SYNCS } from "@/lib/content/home";
import { Terminal } from "./Terminal";
import { SecretsBusTile } from "./SecretsBusTile";

export function WhatItSyncs() {
  return (
    <>
      <section
        aria-label={WHAT_IT_SYNCS.label}
        className="grid grid-cols-1 gap-6 md:grid-cols-2"
      >
        <Terminal />
        <SecretsBusTile />
      </section>
      <p className="mb-16 mt-3 font-body text-[14px] text-text-2">
        {WHAT_IT_SYNCS.caption}
      </p>
    </>
  );
}

export default WhatItSyncs;
