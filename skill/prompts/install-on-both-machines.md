# Install on both machines

> Install agentcookie so my selected browser's sessions reach the Chrome where
> my agent runs. First ask which browser I log in with and confirm the source,
> destination and profile. Explain permissions and any experimental status.
> Review container choices and hold ambiguous sites instead of merging accounts.
> Use an owned live Chrome on a GUI Mac or an explicit loopback attach endpoint
> on Linux. Show profile resolution and readiness, then pair and install only
> the selected flow. Verify accepted versus applied state and report blockers.

For Zen/Firefox, offer the Gecko launcher with the wizard's printed port. The
launcher enables powerful local browser automation; do not start or restart the
browser without permission. Request storage reads make authenticated network
requests. Engine-bound sessions can need another login after an engine swap.

For a GUI Mac sink, use `--destination live_chrome`; the Chrome app launcher
requests restoration through the sink before opening the first window. Do not
write into the user's everyday Chrome profile. Existing configs need an explicit
migration choice, not an automatic override.

Verification: `agentcookie doctor`, `agentcookie status --json`, a selected
user-authorized sync, and Destination `ready` with the latest generation applied.
Pending, repair-needed and held accounts must be reported, not called success.
