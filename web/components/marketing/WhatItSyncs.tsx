// Homepage WhatItSyncs - equal-weight two-tile grid.
//
// Replaces agentcla's TwoPathBento (contributor vs maintainer). The
// agentcookie analog is "what gets synced": cookies (Terminal demo
// of CLIs reading them on the sink) and per-CLI secrets (the v0.14
// adoption manifest + on-disk surface).

import { Terminal } from "./Terminal";
import { SecretsBusTile } from "./SecretsBusTile";

export function WhatItSyncs() {
  return (
    <>
      <section
        aria-label="what agentcookie syncs"
        className="grid grid-cols-1 gap-6 md:grid-cols-2"
      >
        <Terminal />
        <SecretsBusTile />
      </section>
      <p className="mb-16 mt-3 font-body text-[14px] text-text-2">
        two surfaces, one encrypted push. cookies for browser-driving
        agents and adapter-equipped CLIs; secrets bus for everything
        with bearer auth.
      </p>
    </>
  );
}

export default WhatItSyncs;
