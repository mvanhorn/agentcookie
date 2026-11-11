# agentcookie-secret (Python)

Read the per-CLI secrets agentcookie syncs to a machine, from Python. It is
the Python twin of the Go reader in [`pkg/agentcookiesecret`](../../pkg/agentcookiesecret)
and follows the same rules (section 11 of the
[secrets bus spec](../../docs/spec-agentcookie-secrets-bus-v1.md)). It has
no dependencies.

## Install

From a checkout of this repository:

```bash
pip install ./clients/python
```

Or straight from GitHub:

```bash
pip install "git+https://github.com/mvanhorn/agentcookie#subdirectory=clients/python"
```

## Use

```python
from agentcookie_secret import AgentcookieSecretError, load

try:
    env = load("my-pp-cli")
except AgentcookieSecretError:
    env = {}  # fall back to your existing config loading

token = env.get("MY_OAUTH_BEARER")
```

`load` returns a `dict[str, str]`. For each key, the first of these that has
it wins:

1. `~/.agentcookie/secrets/<cli>/secrets.env.sealed`, when sealing is on.
   The reader runs `agentcookie secret env --no-aliases <cli>` to unseal
   it, so the master key stays in agentcookie's Keychain item and the
   `agentcookie` binary must be on `PATH`.
2. `~/.agentcookie/secrets/<cli>/secrets.env`
3. An optional fallback file in the same `KEY=VALUE` format, such as
   `load("my-pp-cli", fallback_path=os.path.expanduser("~/.config/my-pp-cli/auth.env"))`.
   A missing or unparsable fallback file is ignored.
4. Process environment variables.

The bus wins over the environment on purpose: a leftover env var must not
hide a fresher synced value. `KEY=` is an empty value, not a missing key.

`load_detailed` returns the same map plus where each key came from:

```python
from agentcookie_secret import Source, load_detailed

res = load_detailed("my-pp-cli")
if res.sources.get("MY_OAUTH_BEARER") is Source.ENV:
    print("not synced yet; using the environment")
```

## Errors

- `InvalidCLINameError` (also a `ValueError`): the name is not lowercase
  letters, digits and hyphens. Raised before any file is read.
- `AgentcookieSecretError`: `secrets.env` has a syntax error or cannot be
  read, or a sealed file is present but cannot be unsealed (for example,
  `agentcookie` is not on `PATH`). The message names the file, and a syntax
  error also names the line.

A CLI with no bus entry is not an error; you get the fallback file and the
environment.

The reader never writes. Aliases (`agentcookie secret alias`) are not
applied; use `agentcookie secret env <cli>` when you want them.

## Test

```bash
python -m unittest discover -s clients/python
```
