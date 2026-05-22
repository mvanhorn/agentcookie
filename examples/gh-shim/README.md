# gh-shim

Worked example: feed GitHub CLI (`gh`) its auth token from the agentcookie secrets bus.

`gh` is not a printing-press CLI. It's upstream-maintained, distributed via Homebrew, and stores its own auth in `~/.config/gh/hosts.yml`. We do not modify it. Instead, we put a small shim ahead of it on `$PATH` that sources `GH_TOKEN` from the bus and `exec`s the real binary.

## Install

```bash
# 1. Copy the shim somewhere ahead of Homebrew on PATH.
mkdir -p ~/.local/bin
cp examples/gh-shim/gh ~/.local/bin/gh
chmod +x ~/.local/bin/gh

# 2. Make sure ~/.local/bin precedes /opt/homebrew/bin in PATH.
#    (Add this to ~/.zshrc if it isn't already.)
echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.zshrc

# 3. Stage a token in the bus.
echo -n "ghp_xxxxxxxxxxxxxxxxxxxx" | agentcookie secret set gh GH_TOKEN

# 4. Verify.
which gh                  # should print ~/.local/bin/gh, not /opt/homebrew/bin/gh
gh auth status            # should report logged in as the bus-supplied user
```

## How it works

The shim is ~50 lines of bash. On every invocation it:

1. Finds the real `gh` binary at `/opt/homebrew/bin/gh` (or `/usr/local/bin/gh`, `/usr/bin/gh`).
2. If `$GH_TOKEN` is already set in the caller's env, leaves it alone.
3. Otherwise runs `agentcookie secret env gh`, which prints `KEY=VALUE` lines from `~/.agentcookie/secrets/gh/secrets.env`.
4. Exports the gh-relevant keys (`GH_TOKEN`, `GITHUB_TOKEN`, `GH_HOST`, `GH_ENTERPRISE_TOKEN`).
5. `exec`s the real `gh` with the same arguments.

The shim does NOT:
- Read or write `~/.config/gh/hosts.yml`. That's gh's own keystore; we never touch it.
- Filter or transform gh's output. It just prepends env, then gets out of the way.
- Hold the bus open. `agentcookie secret env` is a one-shot read.

## When this matters

Two sink Macs that both want `gh pr list` working without re-running `gh auth login`. The friend stages `GH_TOKEN` once on the source. Both sinks pull it down via the standard cookie+secrets sync. Both sinks have the shim installed. Both sinks now answer `gh auth status` as the source user.

## Removing

```bash
rm ~/.local/bin/gh
agentcookie secret rm gh
```

PATH precedence reverts to `/opt/homebrew/bin/gh`, which uses its own `hosts.yml` again.

## Adapting the shim to another CLI

The same pattern works for any tool that reads an env var for auth. Copy `gh`, rename it to whatever binary you want to wrap, change the `real_*` search paths to point at the real binary, and update the `case` block to whitelist the env vars that CLI cares about. Keep the whitelist tight - exporting unrelated bus values into a third-party process leaks more than it has to.
