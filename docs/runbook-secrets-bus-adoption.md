# Adopting the agentcookie secrets bus in your CLI

This runbook is for CLI authors who want their CLI to read auth tokens / API keys / OAuth bearers from the agentcookie secrets bus instead of from their own bespoke config file.

You do not have to remove your existing config file. The pattern in this runbook layers the bus on top of your existing loader so users keep working through the transition and gradually opt into the bus.

## The contract

agentcookie writes one file per CLI at:

```
~/.agentcookie/secrets/<your-cli-name>/secrets.env
```

Plain `KEY=VALUE` lines, one per line. Mode 0600. When the v0.12 master key is set up on the machine, a sealed twin appears as `secrets.env.sealed` and the plaintext may be absent.

Full grammar + lookup priority is in `docs/spec-agentcookie-secrets-bus-v1.md`.

## Three integration patterns

Pick the one that matches how your CLI is built.

### Pattern A: Go CLI reads its own config TOML / JSON today

Most PP CLIs are here. Add the agentcookie Go reader to your imports and call it at startup before your existing config loader. The bus values override env; your existing config still wins for keys not in the bus, so existing users see no change.

```go
import "github.com/mvanhorn/agentcookie/pkg/agentcookiesecret"

func loadAuth() *Config {
    // 1. Try the agentcookie bus first.
    busEnv, err := agentcookiesecret.LoadWithFallback(
        "my-pp-cli",                                            // your cli name
        filepath.Join(os.Getenv("HOME"), ".config", "my-pp-cli", "config.toml"),
    )
    if err != nil && !errors.Is(err, agentcookiesecret.ErrInvalidCLIName) {
        // sealed file present but master key missing, or similar -
        // fall through to your existing loader
        log.Printf("agentcookiesecret: %v; falling back", err)
    }

    // 2. Now invoke your existing TOML / JSON / env loader. Where it
    //    reads from env vars, read from busEnv first.
    cfg := &Config{
        OAuthBearer:  busEnv["MY_OAUTH_BEARER"],
        OAuthRefresh: busEnv["MY_OAUTH_REFRESH"],
        // ... etc
    }
    if cfg.OAuthBearer == "" {
        cfg.OAuthBearer = os.Getenv("MY_OAUTH_BEARER")
    }
    return cfg
}
```

Key choices:

- The fallback path argument to `LoadWithFallback` is your CLI's existing config file. The reader looks there for any key not in the bus.
- Bus values win over env vars (per spec section 11.2). Do not invert this in your CLI; users who set both expect the bus to override.
- Keep your existing OAuth-login command (`my-pp-cli auth login`). The bus is for delivery, not for issuance. After login, optionally call `agentcookie secret import-from` to mirror the new tokens into the bus.

### Pattern B: Python (or scripting) CLI that reads env vars today

Shell out to `agentcookie secret get` once per key, or eval an env dump:

```bash
#!/usr/bin/env bash
# At the top of your script:
eval "$(agentcookie secret env my-pp-cli 2>/dev/null)"

# Now MY_OAUTH_BEARER and friends are exported. Your script keeps
# reading them from env exactly as it does today.
your-existing-logic
```

For long-running Python processes, do the equivalent once at startup:

```python
import subprocess, os

try:
    out = subprocess.run(
        ["agentcookie", "secret", "env", "my-pp-cli"],
        capture_output=True, text=True, check=True,
    ).stdout
    for line in out.splitlines():
        if "=" in line:
            k, v = line.split("=", 1)
            os.environ.setdefault(k, v)
except Exception:
    pass  # bus not installed or no entry; keep existing env-var behavior
```

`os.environ.setdefault` rather than direct assignment preserves any explicit env var the user set when invoking your CLI.

### Pattern C: CLI that uses its own dotdir (`~/.toolname/`) outside `~/.config/`

This is the Tesla pattern (one config dir for the CLI; a second dotdir for additional artifacts). Use the same Go reader call as Pattern A, but consume both surfaces:

```go
busEnv, _ := agentcookiesecret.Load("tesla-pp-cli")

// File-shaped artifacts (e.g. signing keys) still come from the dotdir.
// The bus carries the env-shaped half; the file-shaped half stays local.
privateKeyPath := filepath.Join(os.Getenv("HOME"), ".tesla", "snowflake-private.pem")

cfg := &Config{
    OAuthBearer:    busEnv["TESLA_OAUTH_BEARER"],
    FleetClientID:  busEnv["TESLA_FLEET_CLIENT_ID"],
    SigningKeyPath: privateKeyPath,  // never in the bus
}
```

The bus replaces the env-shaped half. The file-shaped half (PEMs, certs, etc.) stays under your dotdir and is marked `sync = false` in the manifest if the user wants to keep it local.

## What NOT to do

- **Do not seal at the CLI layer.** Let agentcookie own that. Your CLI reads plaintext + sealed transparently via the reader library; it never touches the master key directly.
- **Do not rewrite the bus file.** The bus is source-of-truth; your CLI is read-only against it. If your CLI rotates a token (e.g. via OAuth refresh), write the new value to your existing config file and let agentcookie's import flow pick it up.
- **Do not interpolate values into other env vars.** The v1 spec forbids `$OTHER` substitution because not every dotenv parser supports it. Your CLI reads keys literally.
- **Do not assume the bus is present.** It is opt-in. Your fallback path (your existing config loader) must continue to work when the bus is empty or absent.
- **Do not name your bus directory with uppercase, dots, or underscores.** Use lowercase letters, digits, and hyphens. `my-pp-cli` good. `my_pp_cli`, `MyPPCli`, `pp.cli` rejected by the reader.

## Five-line diff template

If your CLI currently does:

```go
cfg := loadConfigFromFile("~/.config/my-pp-cli/config.toml")
```

Adopt the bus with three additions:

```go
// (1) Read the bus first.
busEnv, _ := agentcookiesecret.Load("my-pp-cli")

cfg := loadConfigFromFile("~/.config/my-pp-cli/config.toml")

// (2) Let bus values override the file's matching env-shaped fields.
if v := busEnv["MY_OAUTH_BEARER"]; v != "" { cfg.OAuthBearer = v }
if v := busEnv["MY_OAUTH_REFRESH"]; v != "" { cfg.OAuthRefresh = v }
// (3) ... etc per field
```

Compatible with users who haven't adopted the bus (`busEnv` is mostly empty for them) and with users who already use the bus (their tokens win).

## Marking secrets local-only

If your CLI has a secret type that must NOT be synced between machines (per-device signing key, machine-bound credential, anything generated locally and tied to hardware), drop a manifest at:

```
~/.agentcookie/secrets/<your-cli>/manifest.toml
```

with:

```toml
schema_version = 1
display_name = "My PP CLI"

[sync.keys]
MY_LOCAL_SIGNING_KEY_PEM = false
```

Per-key `false` overrides the default. agentcookie's source will exclude that key from sync. The friend can also opt entire categories out via `[sync] default = false`; see section 4.3 of the spec.

## Letting your existing user adopt without re-login

The smoothest migration is:

1. User installs your CLI v2 with bus support. Your CLI's fallback chain still reads the legacy config file.
2. User authenticates as today (`my-pp-cli auth login`). Tokens write to your legacy config file.
3. User runs `agentcookie secret import-from ~/.config/my-pp-cli/config.toml --as my-pp-cli`. The import heuristic maps known field names to canonical UPPER_SNAKE_CASE; unknown fields land under `_unknown_<orig>` so the user can review.
4. Source machine pushes; sink receives. Your CLI on the sink reads from the bus first, falls back to the file (still present from the import source) when keys are missing.
5. Once the user is confident, they can delete the legacy file or leave it alone; the bus takes precedence either way.

## Reference

- Format spec: `docs/spec-agentcookie-secrets-bus-v1.md`
- Audit of how PP CLIs store auth today: `docs/audits/2026-05-22-pp-cli-auth-inventory.md`
- Go reader package: `github.com/mvanhorn/agentcookie/pkg/agentcookiesecret`
- Worked example for non-PP CLIs (gh): `docs/runbook-secrets-bus-gh-example.md`
