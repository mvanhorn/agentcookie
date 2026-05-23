# Hand-off: adopt agentcookie secrets-bus in last30days

This document is written for a Claude Code session (or human contributor) working inside the `mvanhorn/last30days-skill` repo. The goal is to opt last30days into agentcookie's secrets-bus so users who run last30days on multiple Macs get their API keys auto-synced from their primary laptop to their sink machine.

## Context

agentcookie is a peer-to-peer sync system: a "source" machine (your laptop) ships browser cookies and now CLI/skill secrets to a "sink" machine (your Mac mini) over Tailscale. v0.14 introduces an adoption standard: any project that drops a small `agentcookie.toml` manifest gets auto-discovered, and its secrets ship alongside cookies on every push.

last30days is a strong test case because it is a skill, not a CLI, and it stores its secrets at `~/.config/last30days/.env`. Adopting the bus means a user who sets up last30days on one Mac has it working on every other Mac of theirs, with zero re-setup.

## The change

One file. Drop `agentcookie.toml` somewhere users can install it to `~/.agentcookie/manifests/last30days.toml` on their machine.

Two reasonable shapes for the change:

### Option A (minimum viable): ship a sample, let users install by hand

Add `agentcookie.toml` to the repo root with the manifest contents. Document in your README:

```bash
# Optional: enable agentcookie sync (auto-ships your last30days keys to your other Macs).
# Requires agentcookie v0.14+ on both machines.
mkdir -p ~/.agentcookie/manifests
cp agentcookie.toml ~/.agentcookie/manifests/last30days.toml
```

Users who don't run agentcookie ignore the file. Users who do get auto-sync from their next push onward.

### Option B (smoother): install during setup

last30days already has a setup flow that creates `~/.config/last30days/.env`. Hook the manifest install at the same step:

```python
# In whatever script handles last30days setup:
import os, shutil

agentcookie_dir = os.path.expanduser("~/.agentcookie/manifests")
if os.path.exists(os.path.dirname(agentcookie_dir)):  # only when agentcookie is installed
    os.makedirs(agentcookie_dir, exist_ok=True)
    shutil.copy("agentcookie.toml", os.path.join(agentcookie_dir, "last30days.toml"))
    print("Enabled agentcookie sync for last30days.")
```

The "only when agentcookie is installed" check uses presence of `~/.agentcookie/` as a heuristic. Don't install if agentcookie isn't on the machine; that produces inert files that confuse users.

## The manifest contents

```toml
# agentcookie.toml: secrets-bus adoption manifest v2
# See https://github.com/mvanhorn/agentcookie/blob/main/docs/spec-agentcookie-secrets-bus-v2-adoption.md
schema_version = 2
name = "last30days"
display_name = "last30days"
description = "Brand intelligence skill"
project_kind = "skill"
homepage = "https://github.com/mvanhorn/last30days-skill"

[secrets.file]
path = "~/.config/last30days/.env"

[sync]
default = true

[sync.keys]
# These are configuration, not auth. Don't ship them across machines.
SETUP_COMPLETE = false
FROM_BROWSER = false
INCLUDE_SOURCES = false
```

Field notes:

- `name` MUST be lowercase + hyphens only (`last30days` is fine; `last30days_v2` is not - underscores rejected).
- `[secrets.file].path` MUST start with `~/` and not contain `..`. agentcookie's parser hard-rejects path traversal at discovery time.
- `[sync].default = true` means every key in the .env ships by default. The `[sync.keys]` block excludes the three config-shaped keys (SETUP_COMPLETE, FROM_BROWSER, INCLUDE_SOURCES) which aren't secrets.
- API keys ship by default: ANTHROPIC_API_KEY, OPENAI_API_KEY, RAINFOREST_API_KEY, SCRAPECREATORS_API_KEY, etc.

If you add new keys to last30days's .env in the future, decide per-key whether to ship them. Anything secret should default-ship (no entry in `[sync.keys]`). Anything user-local-only should be `<KEY> = false`.

## Testing

Verify on a user machine that has agentcookie installed:

```bash
# 1. Install the manifest (whichever path Option A or B you chose).

# 2. Confirm agentcookie sees it:
agentcookie discover
# Expected: a row for `last30days` with tier explicit-manifest and read-in-place
# pointing at ~/.config/last30days/.env.

# 3. Push once and confirm sink received it:
agentcookie source --once
ssh other-mac "agentcookie secret list"
# Expected: `last30days` appears with the expected keys, minus the three
# that [sync.keys] excludes.
```

## Non-goals (do not do)

- Do not add a Python dependency on agentcookie. last30days's code does not need to read the bus; agentcookie reads last30days's existing `.env` in place. The manifest is the only thing last30days ships.
- Do not modify `~/.config/last30days/.env`'s schema or format. The bus reads it as-is.
- Do not auto-install the manifest on machines without agentcookie. Inert files confuse users.

## When this lands

Users who run both last30days and agentcookie on multiple machines will type their API keys once (on the primary laptop, into last30days's normal `.env` setup) and have them appear on every sink machine automatically. The bus tracks the source file, so rotating a key in `.env` ships the rotation on the next push - no `import-from` re-run required.

## Questions

The agentcookie repo issues are at `https://github.com/mvanhorn/agentcookie/issues`. Spec ambiguities, weird edge cases ("our key name has a hyphen"), or "the path field doesn't fit our layout" are valid issues there.

Full spec for reference: [docs/spec-agentcookie-secrets-bus-v2-adoption.md](https://github.com/mvanhorn/agentcookie/blob/main/docs/spec-agentcookie-secrets-bus-v2-adoption.md).
