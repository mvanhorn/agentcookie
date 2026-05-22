# Worked example: feeding GitHub CLI from the secrets bus

This runbook walks through end-to-end adoption of the agentcookie secrets bus for a real non-PP CLI: GitHub's `gh`. The goal is to show that the bus is a publicly consumable contract, not a PP-CLI-only mechanism.

The full shim + per-shim README lives at `examples/gh-shim/`. This document is the narrative version: what happens, in what order, why.

## The setup

- Two Macs on Tailscale, with cookie sync already working.
- `gh` installed on both via Homebrew at `/opt/homebrew/bin/gh`.
- Source Mac is logged into GitHub via `gh auth login` (creates `~/.config/gh/hosts.yml` with a token).
- Sink Mac has no `hosts.yml` and `gh auth status` reports "not logged in."

We want sink to use the same token without re-running browser-flow login.

## Step 1: capture the source token into the bus

`gh` stores its OAuth bearer at `~/.config/gh/hosts.yml`:

```yaml
github.com:
    user: mvanhorn
    oauth_token: ghu_xxxxxxxxxxxxxxxxxxxx
    git_protocol: ssh
```

We can either:

- (a) Extract the token by hand and `agentcookie secret set gh GH_TOKEN`, or
- (b) Use `agentcookie secret import-from ~/.config/gh/hosts.yml --as gh` and let the heuristic canonicalize.

The import heuristic does not yet recognize gh's specific YAML shape (it's tuned for JSON + TOML), so (a) is the path of least friction. One line:

```bash
yq -r '."github.com".oauth_token' ~/.config/gh/hosts.yml | agentcookie secret set gh GH_TOKEN
```

Verify it landed:

```bash
agentcookie secret list
# gh
#   GH_TOKEN
```

The bus now holds the gh token at `~/.agentcookie/secrets/gh/secrets.env`, mode 0600, single line `GH_TOKEN=ghu_xxxx...`.

## Step 2: install the shim on sink

The shim is a 50-line bash script that sits ahead of `gh` on `$PATH`. On invocation it reads `agentcookie secret env gh` and exports `GH_TOKEN` before `exec`ing the real binary.

```bash
mkdir -p ~/.local/bin
cp examples/gh-shim/gh ~/.local/bin/gh
chmod +x ~/.local/bin/gh
echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.zshrc
```

After a new shell, `which gh` should print `~/.local/bin/gh` rather than `/opt/homebrew/bin/gh`.

## Step 3: let sync happen

On source, run agentcookie's source (or wait for the watcher). The push includes both cookies AND the bus payload for the `gh` CLI. The sink writes `~/.agentcookie/secrets/gh/secrets.env` with the same `GH_TOKEN=...` line.

## Step 4: verify on sink

```bash
gh auth status
# github.com
#   Logged in to github.com account mvanhorn (~/.agentcookie/secrets/gh/secrets.env)
#   Active account: true
#   Git operations protocol: https
#   Token: ghu_************************
```

The shim sourced GH_TOKEN; `gh` saw a populated env var; the binary trusted it without checking `hosts.yml`.

```bash
gh pr list --limit 5
# real PRs from the source's GitHub account
```

## What this proves

1. The bus is a real, publicly consumable contract. A third-party CLI we do not control consumed it via a tiny shim, with no changes to its codebase.
2. Resolution priority works as specified: shim respects pre-set GH_TOKEN, falls back to bus, falls back to gh's own keystore.
3. Sync is atomic: cookies and secrets ship in the same envelope, so a friend gets logged-in browser + logged-in `gh` from a single source push.

## Failure modes worth knowing

- **Bus is empty on sink.** The shim's `agentcookie secret env gh` returns nothing; the shim exports nothing; the real `gh` falls through to its own `hosts.yml` (which on a fresh sink doesn't exist). `gh auth status` reports "not logged in." This is the correct degraded behavior; no surprise auth.
- **PATH order wrong.** If `/opt/homebrew/bin/gh` precedes `~/.local/bin/gh`, the shim never runs and the bus is silently ignored. `which gh` is the diagnostic.
- **Token expired or revoked.** The shim cannot detect this; it just exports whatever the bus says. `gh auth status` will return 401. Recovery: re-run `gh auth login` on source, then `agentcookie secret import-from` again.
- **Shim conflicts with `gh extension` machinery.** gh's extension subsystem also lives under `~/.local/share/gh/`. The shim doesn't touch that. Tested with `gh extension list` and `gh copilot`.

## Adapting to other CLIs

The shim template generalizes to anything that:
- Reads auth from a single env var, AND
- Is distributed as a binary you don't want to modify.

Examples this would work for, with the same ~50 lines:

| CLI | Env var | Bus directory |
|-----|---------|--------------|
| `gh` | `GH_TOKEN` | `gh` |
| `glab` | `GITLAB_TOKEN` | `glab` |
| `aws` | `AWS_ACCESS_KEY_ID` + `AWS_SECRET_ACCESS_KEY` | `aws` |
| `op` (1Password CLI) | `OP_SESSION_<acct>` | `op-<acct>` |
| `vault` | `VAULT_TOKEN` | `vault` |

For tools that don't read an env var (some CLIs only read their own config file), the Tesla pattern in `docs/runbook-secrets-bus-adoption.md` Pattern C is the right reference instead.

## Reference

- The shim itself: `examples/gh-shim/gh`
- Installation walkthrough: `examples/gh-shim/README.md`
- Format spec: `docs/spec-agentcookie-secrets-bus-v1.md`
- General CLI-author runbook: `docs/runbook-secrets-bus-adoption.md`
