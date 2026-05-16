# The "tell your agent this" prompt

Copy this into Claude Code, OpenClaw, Hermes, Codex, Cursor, or any agent that can run shell commands locally and over SSH:

> Install agentcookie on this laptop and my Mac mini so my Chrome sessions sync continuously. Use Tailscale to find the Mac mini. Confirm with me which machine is the source and which is the sink, then run the full install end to end. After install, verify both daemons are running and tell me what you see.

The agent should:

1. Detect Tailscale and identify the peer machine.
2. Confirm source vs sink with you.
3. Run `agentcookie wizard install --as source` here, in the background.
4. Read the pairing code from `~/.agentcookie/pairing.json` once it appears.
5. SSH to the Mac mini and run `agentcookie wizard install --as sink ...` with that code.
6. Report back that both daemons are up.

Total elapsed time: about 30 seconds. You do not need to be at the Mac mini's screen. No Keychain prompts will fire on the Mac mini.

## When the prompt is not enough

If the agent gets stuck, the most common reasons (in rough probability order):

1. SSH from your laptop to your Mac mini does not work passwordlessly. Fix by setting up SSH keys, or use Tailscale SSH (`tailscale ssh` is the command).
2. Google Chrome is not installed on the Mac mini. The wizard needs it. Install Chrome.
3. Go is not installed on the Mac mini, so `go install` fails. Either install Go (`brew install go`) or transfer a prebuilt binary from the laptop via `scp`.
4. Tailscale ACLs are restrictive. Default Tailscale config allows everything between your own devices; if you have custom ACLs, allow tailnet-internal traffic on ports 9998 (pairing) and 9999 (sync).

After fixing, re-paste the prompt. The wizard is idempotent and will pick up where it left off.
