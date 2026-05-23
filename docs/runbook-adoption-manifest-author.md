# Runbook: shipping an agentcookie.toml from your project

You're the author of a CLI, skill, or service that uses some auth state. Your users run agentcookie between a primary laptop and one or more sink Macs. You want those users to get auto-sync of your tool's secrets without typing `agentcookie secret import-from` per machine.

This runbook walks you through the one-file change.

## Prerequisites

- Your tool stores secrets in an env-shaped file somewhere stable (e.g., `~/.config/<tool>/.env` or `~/.config/<tool>/config.toml`).
- Your users are on agentcookie v0.14+. (Older agentcookie versions ignore the manifest harmlessly.)

If your auth state is keychain-only, a JWT minted from an OAuth dance per session, or distributed across multiple files, see the "Edge cases" section below.

## Step 1: write the manifest

Pick the template that matches your project from `examples/`:

- `examples/adoption-last30days/` for skills that read a dotenv.
- `examples/adoption-third-party-cli/` for CLIs.

Or hand-write:

```toml
schema_version = 2
name = "my-tool"              # required, lowercase + hyphens only
display_name = "My Tool"      # required, human label
description = "What it does"  # optional
project_kind = "cli"          # optional: cli | skill | service | other
homepage = "https://..."      # optional

[secrets.file]
path = "~/.config/my-tool/.env"

[sync]
default = true                # or false for an explicit allowlist

[sync.keys]
NOT_A_SECRET = false          # exclude individual keys
```

Validate before shipping:

```go
import "github.com/mvanhorn/agentcookie/pkg/agentcookieadoption"
agentcookieadoption.Validate(m)
```

Or just install it locally and run `agentcookie discover --verbose` - errors surface there.

## Step 2: ship the manifest from your installer

The simplest path: put `agentcookie.toml` in your repo root and copy it during your install flow. The install only happens when agentcookie is already present (no-op for users who don't run it).

```bash
# In your install script:
if [ -d "$HOME/.agentcookie" ]; then
    mkdir -p "$HOME/.agentcookie/manifests"
    install -m 0644 agentcookie.toml "$HOME/.agentcookie/manifests/my-tool.toml"
fi
```

Or hand the file to the user in your README and let them install it themselves.

## Step 3: verify

On a machine with both your tool and agentcookie installed:

```bash
agentcookie discover
```

Your tool should appear with tier `explicit-manifest`. If it doesn't, run with `--verbose` to see why it was skipped.

```bash
# Trigger a push manually:
agentcookie source --once

# Confirm on the sink:
ssh other-mac "agentcookie secret list"
```

Your tool's slug should appear with the expected key set.

## Edge cases

### My secrets are in macOS Keychain

Manifest reserves `[secrets.keychain]` for v2.1. Until then, you have two options:

1. Wait for v2.1 (no ETA).
2. On install, export the keychain entries into a `~/.config/<tool>/.env` your tool also reads, and point the manifest at that file. This duplicates state but works today.

### My auth is a JWT minted per session, not a stable token

The bus is best for stable tokens (long-lived bearers, refresh tokens, API keys). Short-lived JWTs that you mint on every invocation are a poor fit - shipping a stale JWT just delays the inevitable re-mint on the sink. Consider:

1. Shipping the refresh token instead and letting each side mint its own JWT.
2. Shipping the OAuth client_id + client_secret (which are stable) and skipping the per-session JWT.

### My secrets are spread across multiple files

The manifest can point at exactly one `[secrets.file]`. Two ways out:

1. Consolidate into one file on install.
2. Ship two separate manifests with different `name` slugs (e.g., `my-tool-auth` and `my-tool-config`).

### I have file-shaped secrets (PEMs, certs)

The bus only carries env-shaped key-value pairs. File-shaped artifacts (signing keys, cert chains) stay outside the bus per spec section 1.2. The Tesla CLI is the canonical example: its PEM signing key stays in `~/.tesla/snowflake-private.pem`; only the OAuth bearer ships via the bus.

### My users have multiple installs of my tool with different auth (e.g., dev + prod)

The bus is single-account by design. Multi-account support is a documented v1.1 spec gap (see `docs/audits/2026-05-22-pp-cli-auth-inventory.md` Section "v1.1 Spec Gaps"). Today, work around it by giving each install a distinct slug (`my-tool-dev`, `my-tool-prod`) and shipping two manifests.

## What NOT to do

- Don't import `pkg/agentcookiesecret` into your tool unless you specifically want your tool to read from the bus directly (most don't need to; the bus delivers to your existing file location).
- Don't sign the manifest. v2.0 ignores `signed_by`; v2.1 will define a verification flow.
- Don't ship the manifest from a public CDN or `curl | bash` your installer's manifest into place. Manifests are inert data but their `[secrets.file].path` is a path-traversal vector that agentcookie validates - keep the source you ship from trusted.
- Don't put real secrets in the manifest. The manifest is a *pointer* to where secrets live; the secrets themselves remain in your tool's existing file.

## When this all lands

Your users get cross-machine sync for free. They install your tool on their primary laptop, set up their secrets once, and every sink Mac with agentcookie picks them up on the next push. Rotating a key in your tool's existing config file ships the rotation on the next push - no agentcookie commands required.

## Reference

- Spec: `docs/spec-agentcookie-secrets-bus-v2-adoption.md`
- Author helper library: `pkg/agentcookieadoption`
- Discovery command: `agentcookie discover`
- Removal: `agentcookie secret revoke <name>`
- Issues: https://github.com/mvanhorn/agentcookie/issues
