# Example: last30days adoption manifest

This directory holds a drop-in `agentcookie.toml` for last30days. It's the same file referenced in `docs/handoff-guides/for-last30days-team.md`.

## Install

```bash
mkdir -p ~/.agentcookie/manifests
cp agentcookie.toml ~/.agentcookie/manifests/last30days.toml
```

## Verify

```bash
agentcookie discover
# Expected row: last30days  explicit-manifest  ~/.agentcookie/manifests/last30days.toml  ~/.config/last30days/.env  ...
```

## Push once and check the sink

```bash
agentcookie source --once
ssh other-mac "agentcookie secret list"
# Expected: last30days with all .env keys, minus the three excluded by [sync.keys].
```

## Remove

```bash
agentcookie secret revoke last30days --force
# Or manually:
rm ~/.agentcookie/manifests/last30days.toml
```
