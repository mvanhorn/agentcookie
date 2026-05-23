# Example: third-party CLI adoption manifest

Template for any CLI that:

- Reads auth from a single dotenv-shaped file.
- Wants its auth state synced from a primary machine to a sink machine via agentcookie.

## Adapt to your CLI

Edit `agentcookie.toml`:

1. Change `name` to your CLI's slug (lowercase, hyphens only, no underscores or dots).
2. Change `display_name` to whatever you want to surface in `agentcookie discover` output.
3. Change `[secrets.file].path` to the path your CLI actually reads.
4. Decide on `[sync].default`:
   - `true` if every key in the file is meant to ship (typical for API tokens).
   - `false` if you want a strict allowlist via `[sync.keys]`.
5. Optionally add `[sync.keys]` to exclude config-shaped keys that aren't real secrets.

## Install

```bash
mkdir -p ~/.agentcookie/manifests
cp agentcookie.toml ~/.agentcookie/manifests/my-tool.toml
```

## Verify

```bash
agentcookie discover
```

You should see your CLI listed under tier `explicit-manifest`.

## Ship it from your project

If you're shipping a CLI, you can include the manifest in your installer:

```bash
# Inside your CLI's install script:
if [ -d "$HOME/.agentcookie" ]; then
    mkdir -p "$HOME/.agentcookie/manifests"
    install -m 0644 agentcookie.toml "$HOME/.agentcookie/manifests/my-tool.toml"
fi
```

The `if [ -d "$HOME/.agentcookie" ]` check skips the install for users who don't run agentcookie. No-op for them; auto-sync for everyone else.

## Programmatic generation

For tooling that emits manifests, import the helper library:

```go
import "github.com/mvanhorn/agentcookie/pkg/agentcookieadoption"

m := &agentcookieadoption.Manifest{
    SchemaVersion: 2,
    Name:          "my-tool",
    DisplayName:   "My Tool",
    ProjectKind:   "cli",
    Secrets: agentcookieadoption.Secrets{
        File: &agentcookieadoption.SecretsFile{Path: "~/.config/my-tool/auth.env"},
    },
}
if err := agentcookieadoption.WriteTo(m, "agentcookie.toml"); err != nil {
    log.Fatal(err)
}
```

`agentcookieadoption.Validate(m)` runs the same checks the agent does at discovery time.
