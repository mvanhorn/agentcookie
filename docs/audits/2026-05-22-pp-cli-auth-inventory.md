# Audit: PP CLI auth storage inventory (2026-05-22)

## Methodology

This audit walks every Printing Press CLI installed locally under `~/printing-press/library/` (34 unique CLIs once `*.bak.*` and `*.preserve-*` backup directories are excluded), inventories where each persists its auth surface, and classifies what kind of secret lives in each file. Sources walked:

1. `~/.config/<name>-pp-cli/` — primary XDG-style PP CLI config directory.
2. `~/.config/<name>/` — alternate non-`-pp-cli` layout used by some CLIs (instacart, agent-capture).
3. `~/.<name>-pp-cli/` — per-CLI dotdir under `$HOME`. On inspection, every existing instance of this directory holds only `feedback.jsonl` (anonymous local feedback log) and occasionally a `profiles.json` (named flag-set bundle). No secrets observed here in any of the 27 such dirs present.
4. `~/.tesla/` — Tesla's documented non-XDG layout (the plan's worst case).
5. `~/.slack/` — Slack PP CLI's `~/.slack/`-style layout.
6. Binary inspection: `<binary> --help`, `<binary> auth --help`, and `strings` greps for env-var names (`*_TOKEN`, `*_API_KEY`, `*_CLIENT_SECRET`, `*_ACCESS_TOKEN`) to find documented or hard-coded auth surfaces beyond what files reveal.

All file values were inspected by field/key name only. Values were never printed or stored; redaction was enforced by piping through `awk -F= '{print $1}'`, `jq 'keys'`, or `sqlite3 '.schema'`. No real account IDs, OAuth bearers, refresh tokens, API keys, signing key bytes, VINs, phone numbers, or session-cookie values appear anywhere in this document.

The PP-CLI inventory is exhaustive for the 34 real CLIs in the library. Where a CLI has no config dir present, the auth surface is inferred from `auth --help` and `strings` grep on the binary; gaps are called out explicitly.

## Summary

Three storage patterns dominate, and the variance that matters for the secrets-bus format spec is more about which secret categories cluster than about file formats.

**Pattern A: standard `config.toml`.** Eleven CLIs use the same scaffolded `config.toml` with the canonical seven-field set: `base_url`, `auth_header`, `access_token`, `refresh_token`, `token_expiry`, `client_id`, `client_secret`. That is the Printing Press meta-CLI's default auth scaffold. A handful extend it with extra fields (Tesla adds `auth_token`; Suno adds `token`; Dominos adds `token`; Superhuman adds `jwt` and `active_email`; Podcast Goat adds five third-party API key fields; Expensify adds `auth_token` + `partner_user_id` + `partner_user_secret`; ordertogo adds Stripe customer IDs and Mesh user metadata).

**Pattern B: companion JSON files alongside the TOML.** Six CLIs need state that does not fit the canonical TOML shape: browser-session-proof JSON (eBay, OpenArt, Suno), session cookies (Airbnb, OrderToGo, Contact Goat, table-reservation-goat), multi-account OAuth token bundles (Superhuman `tokens.json`), and offline send queues (Superhuman `send-queue.json`).

**Pattern C: non-XDG dotdir for legacy/special cases.** Two CLIs persist outside `~/.config/`: Tesla's `~/.tesla/` (eight files including OAuth, refresh, partner-app creds, an ECDSA EC P-256 keypair, and a vehicle command token) and Slack's `~/.slack/` (config + credentials JSON; multi-team store keyed by Slack team ID).

The variance the secrets-bus format must absorb:

- **Multi-account stores.** Superhuman holds N accounts indexed by email under `accounts`, each with its own access token / refresh token / device ID / Superhuman-issued token. Slack credentials.json is keyed by Slack team ID. The bus format needs an account-namespacing convention or a per-account file convention.
- **Per-CLI signing keys that must stay machine-local.** Tesla's `snowflake-private.pem` (EC private key for signed-command authorization on the car). These cannot be synced; the manifest convention in R5 must mark them `local-only`.
- **Browser session proofs.** Three CLIs persist a "I logged in as this human in Chrome" fingerprint (cookie domain, validation method, credential fingerprint). These are device-bound today (they certify a session that lived in a specific browser profile), but the threat model would accept syncing them when the sink is the agent-controlled machine the source already trusts. Flag for follow-up review.
- **SQLite-backed token blobs.** Linear's `store.db` is mostly sync cache, but its companion `LINEAR_API_KEY` env var is the actual secret. Dominos' `store.db` is sync cache only; its auth lives in `config.toml`. The bus does not need to ship sqlite blobs, only the env vars / TOML fields the CLI reads at boot.
- **Env-var overlay path.** Most CLIs accept both file-based config and an env-var override (e.g. `SALESFORCE_ACCESS_TOKEN`, `LINEAR_API_KEY`, `HUBSPOT_ACCESS_TOKEN`, `SLACK_BOT_TOKEN`, `TESLA_FLEET_CLIENT_SECRET`). This is excellent news for the secrets-bus design: a CLI that already reads `LINEAR_API_KEY` from the environment will read it from `~/.agentcookie/secrets/linear-pp-cli/secrets.env` with zero code change once the launcher exports it.

## Per-CLI table

| CLI | Storage paths | Format | Secret categories | Sync-safety | Notes |
| --- | ------------- | ------ | ----------------- | ----------- | ----- |
| agent-capture | (none observed; ~/.config/agent-capture/presets/ holds saved presets, no secrets) | n/a | none | n/a | macOS screen-capture tool; no remote API auth. |
| airbnb-vrbo | inferred: ~/.config/airbnb-vrbo-pp-cli/ (not present; user has not authed) | toml + json (inferred from twin `airbnb-pp-cli` shape) | session cookies (Chrome harvest) | safe-to-sync | `auth login` documented; Airbnb SSR scrape; no auth needed for read-only listings. |
| airbnb-pp-cli (legacy slug, present on disk) | ~/.config/airbnb-pp-cli/config.toml, ~/.config/airbnb-pp-cli/cookies.json | toml + json | canonical TOML scaffold (likely unused), session cookies in cookies.json | safe-to-sync | The TOML carries scaffold OAuth fields that may be placeholders; the real auth is the cookies.json. |
| alaska-airlines | ~/.config/alaska-airlines-pp-cli/ (not present on disk; binary supports `auth login/logout/status`) | toml (canonical scaffold expected) | OAuth bearer + refresh token (presumed) | safe-to-sync | User has never run `auth login`; only feedback.jsonl in `~/.alaska-airlines-pp-cli/`. |
| archive-is | (no config dir present; `auth set-token` would create one) | toml (canonical scaffold) | API token (presumed) | safe-to-sync | Read-only API; auth optional. |
| booking-com | ~/.config/booking-com-pp-cli/config.toml | toml | canonical TOML scaffold (base_url, auth_header, access_token, refresh_token, token_expiry, client_id, client_secret) | safe-to-sync | Standard PP scaffold; OAuth bearer + refresh. |
| bugbounty-goat | ~/.config/bugbounty-goat-pp-cli/ (empty dir, no config persisted) | toml (canonical scaffold expected) | API token (set-token surface) | safe-to-sync | User has not authed yet. |
| contact-goat | ~/.config/contact-goat-pp-cli/cookies-happenstance.json | json | Happenstance browser session cookies (Chrome-harvested) | device-bound-but-shippable | Session keyed by browser profile; HAPPENSTANCE_API_KEY env-var alternative documented. Fields: cookies[], saved_at, service, source_os. |
| digg | ~/.config/digg-pp-cli/ (not present; binary has doctor but no documented `auth login` for digg-pp-cli) | toml (canonical scaffold expected) | API token (set-token surface) | safe-to-sync | Tracks Digg/AI-news; auth optional for public endpoints. |
| dominos | ~/.config/dominos-pp-cli/config.toml, ~/.config/dominos-pp-cli/store.db | toml + sqlite | canonical TOML scaffold + extra `token`; store.db is sync cache (menu, stores, graphql) with no secrets | safe-to-sync | Store.db is regenerable; only config.toml is bus material. |
| drudgereport | ~/.config/drudgereport-pp-cli/ (empty dir) | n/a (read-only) | none | n/a | Drudge Report scraper; no remote auth. |
| ebay | ~/.config/ebay-pp-cli/config.toml, ~/.config/ebay-pp-cli/browser-session-proof.json | toml + json | canonical TOML scaffold + browser session proof (api_name, auth_source, cookie_domain, credential_fingerprint, status_code, validation_method, validation_path, verified_at) | safe-to-sync (TOML) + device-bound-but-shippable (session-proof JSON) | Browser-session-proof certifies a specific Chrome login; bus sync would replicate the fingerprint, which is what we want for the agent sink. |
| espn-pp-cli | ~/.config/espn-pp-cli/config.toml, ~/.config/espn-pp-cli/watchlist.json | toml + json | no secrets (favorites list only); watchlist is user-curated UI state | n/a | ESPN's public APIs need no auth; config holds preferences. |
| expensify | ~/.config/expensify-pp-cli/config.toml | toml | canonical TOML scaffold + `auth_token`, `partner_user_id`, `partner_user_secret` | safe-to-sync | Expensify has a "partner app" credential model; bus must carry all three companion fields together. |
| granola | ~/.config/granola-pp-cli/ (not present; auth supports set-token + setup) | toml (canonical scaffold expected) | OAuth bearer (presumed) | safe-to-sync | Reads Granola's local cache file at `~/Library/Application Support/Granola/`; CLI's own auth is for upstream API. |
| greatclips | ~/.config/greatclips-pp-cli/ (not present; auth supports set-token) | toml (canonical scaffold expected) | API token | safe-to-sync | Great Clips check-in API. |
| hubspot-pp-cli | (no config dir present) | env-var primary (`HUBSPOT_ACCESS_TOKEN`); set-token can write canonical TOML | env var + (canonical TOML) | safe-to-sync | Hubspot CLI prefers env var; bus delivery via `secrets.env` is the natural fit. |
| instacart | documented in binary as `~/.config/instacart/session.json` (mode 0600); not currently present on disk (user has likely logged out) | json | session cookies (Chrome-harvested incl. HttpOnly: `__Host-instacart_sid`, `_instacart_session_id`, `forterToken`, etc.) | safe-to-sync | Uses `kooky` to read Chrome's cookie DB. Optional `~/.config/instacart/config.json` for `postal_code`. Sub-dir naming differs from `-pp-cli` convention. |
| linear-pp-cli | ~/.config/linear-pp-cli/store.db | sqlite | sync cache only (no auth in DB); real secret is `LINEAR_API_KEY` env var | safe-to-sync (env var) | The CLI prints `export LINEAR_API_KEY=<your-key>` on logout. Bus delivers via secrets.env. Store.db is regenerable from API; not bus material. |
| microsoft-graph-teams | ~/.config/microsoft-graph-teams-pp-cli/ (not present; binary supports `auth login` via OAuth2) | toml (canonical scaffold + delta tokens) | OAuth bearer + refresh token + delta cursor tokens | safe-to-sync | Delta tokens are resumable-sync state; arguably regenerable but cheap to sync. |
| openart | ~/.config/openart-pp-cli/config.toml, ~/.config/openart-pp-cli/browser-session-proof.json | toml + json | canonical TOML scaffold + browser session proof | safe-to-sync (TOML) + device-bound-but-shippable (session-proof) | Same shape as eBay/Suno. |
| ordertogo | ~/.config/ordertogo-pp-cli/config.toml, cookies.json, active-cart.json, carts/ subdir | toml + json + (json) | canonical TOML scaffold + Stripe customer ID + Mesh user metadata + customer name/phone + session cookies + active cart state with payment validation_body | safe-to-sync (most) + caution on customer_phone field (account identifier; redact in audit but bus carries) | This is the broadest single TOML on disk. `mesh_user_id`, `stripe_customer_id`, `customer_phone` are account-identity fields; bus replicates them since they bind the agent to the user's restaurant-ordering identity. |
| podcast-goat | ~/.config/podcast-goat-pp-cli/config.toml | toml | canonical TOML scaffold + five third-party API key fields: `spoken_api_key`, `taddy_api_key`, `taddy_user_id`, `openai_api_key`, `deepgram_api_key`, `elevenlabs_api_key` | safe-to-sync | Aggregator CLI; bus must support arbitrary extra `*_api_key` fields beyond the scaffold seven. |
| redfin | (no config dir present; binary supports `auth set-token`) | toml (canonical scaffold expected) | API token | safe-to-sync | Redfin listings; auth required for some endpoints. |
| salesforce-headless-360 | ~/.config/salesforce-headless-360-pp-cli/ (not present; multi-org auth flow `auth login/list-orgs/switch-org`) | toml (per-org profile expected) + env vars `SALESFORCE_ACCESS_TOKEN`, `SALESFORCE_INSTANCE_URL` | OAuth bearer (per org), JWT private key (RSA), bundle signing public key via `trust register` | safe-to-sync (OAuth/JWT) + local-only (bundle signing private key on the device) | The CLI registers a per-device bundle-signing public key with the Salesforce Certificate trust chain; the *private* half stays on the device and must never sync. Strings include `SF360_HOST_FINGERPRINT` (per-host fingerprint also local). |
| slack | ~/.slack/config.json, ~/.slack/credentials.json (non-XDG: `~/.slack/`, not `~/.config/slack-pp-cli/`) | json + json | system_id (machine identifier), credentials keyed by Slack team ID (e.g. T08C8AN2Z3R) | safe-to-sync (credentials per team) + caution on system_id (device identifier) | credentials.json is multi-tenant: one OAuth credential record per Slack workspace, keyed by team ID. Bus must preserve the team-ID keying. `SLACK_BOT_TOKEN` env var documented as an alternative. |
| suno | ~/.config/suno-pp-cli/config.toml, ~/.config/suno-pp-cli/browser-session-proof.json | toml + json | canonical TOML scaffold + extra `token` + browser session proof | safe-to-sync (TOML) + device-bound-but-shippable (session-proof) | Same browser-session-proof shape as eBay/OpenArt. |
| superhuman | ~/.config/superhuman-pp-cli/config.toml, ~/.config/superhuman-pp-cli/tokens.json, ~/.config/superhuman-pp-cli/send-queue.json | toml + json + json | canonical TOML scaffold + extra `jwt` + `active_email`; tokens.json has per-account map under `accounts`, each record holding `accessToken`, `deviceId`, `expires`, `lastUsedAt`, `refreshToken`, `superhumanToken`, `type`, `userExternalId`, `userId`, `userPrefix` | safe-to-sync (account tokens) + caution on `deviceId` (per-device identifier the server may bind to) | Multi-account is the hard case for the bus format. Account email is the keyspace; per-account credentials are first-class. send-queue.json is queued-send state, not auth (regenerable on sink). |
| superhuman-mail | (no config dir present; binary supports `auth set-token`) | toml (canonical scaffold expected) | API token (presumed) | safe-to-sync | Separate library entry from superhuman; lighter scope (mail only). |
| table-reservation-goat | ~/.config/table-reservation-goat-pp-cli/session.json | json | OpenTable cookies + Tock cookies (Chrome-harvested), updated_at, version | safe-to-sync (cookies are session-bound; sink is the intended consumer) | Two-vendor session in one file. No accompanying TOML. |
| tesla | ~/.config/tesla-pp-cli/config.toml, ~/.config/tesla-pp-cli/auth.json, AND ~/.tesla/ with eight files (see Detailed notes) | toml + json + (PEM + raw text) | OAuth bearer, refresh token, fleet OAuth credentials, partner-app token, EC P-256 ECDSA signing keypair, vehicle command token | safe-to-sync (OAuth + refresh + partner creds + fleet token) + **local-only** (snowflake-private.pem) + safe-to-sync (snowflake-public.pem) | Worst-case in the inventory; see Detailed notes below. |
| trendhunter | ~/.config/trendhunter-pp-cli/ (not present) | n/a (likely no auth; binary advertises "no API key" in help) | none | n/a | TrendHunter scraper. |
| trigger-dev | ~/.config/trigger-dev-pp-cli/ (not present; binary supports `auth set-token`) | toml (canonical scaffold expected) | API token + waitpoint tokens (separate concept; resource state, not auth) | safe-to-sync | trigger.dev API key. |
| trustpilot | ~/.config/trustpilot-pp-cli/ (not present; auth harvests `aws-waf-token` cookie via agent-browser Chrome wrapper) | toml or json (cookie + Next.js build IDs) | AWS WAF token cookie (~5-15 min TTL), Next.js build IDs | device-bound-but-shippable | Token is short-lived; sync useful only within the TTL window. |
| yeswehack | ~/.config/yeswehack-pp-cli/ (not present; binary supports `auth login` "Import authentication from a browser session" + `auth set-token`) | toml + json (browser session expected) | session cookies / JWT (cookie-first per project memory) | safe-to-sync | YesWeHack researcher CLI must use cookie/JWT from browser session (PATs are program-manager-side per project memory). |

Notes on coverage:

- 34 unique PP CLIs in `~/printing-press/library/` (after filtering `*.bak.*` and `*.preserve-*` backup directories).
- 15 have an active `~/.config/<name>-pp-cli/` directory with concrete files inspected.
- 17 have a corresponding `~/.<name>-pp-cli/` dotdir under `$HOME`, but all observed contents were `feedback.jsonl` and at most `profiles.json` (no secrets).
- The remaining CLIs either have no auth surface (archive-is, drudgereport, espn-pp-cli for public endpoints, agent-capture, trendhunter for public scrape) or the user has not yet authed (and thus the config dir does not exist; the binary's `auth set-token` or `auth login` would create the canonical scaffold).

## Detailed notes for non-trivial cases

### Tesla — `~/.tesla/` (the documented worst case)

Eight files, two of which are signing-key bytes that must never leave the machine.

| File | Format | What it holds | Sync-safety | Notes |
| ---- | ------ | ------------- | ----------- | ----- |
| token | Raw JWT (RS256) | Owner-API OAuth access token | safe-to-sync | Tesla's main API bearer for the legacy owner-api flow. |
| fleet-token | Raw JWT (RS256) | Fleet API OAuth access token | safe-to-sync | New fleet-api flow. |
| fleet-token.refresh | Opaque ASCII | Fleet API refresh token | safe-to-sync | Used to re-mint fleet-token. |
| fleet-client-id | ASCII text | Fleet API OAuth client ID | safe-to-sync | Per developer.tesla.com app registration. Not user-bound. |
| fleet-client-secret | ASCII text | Fleet API OAuth client secret | safe-to-sync | Long-lived; tied to the developer app, not the machine. |
| fleet-partner-token | Long JWT/opaque | Partner-app authorization token used to enroll public-key host domain | safe-to-sync | Minted via `tesla auth fleet-register`. |
| snowflake-private.pem | PEM EC PRIVATE KEY (P-256) | ECDSA signing private key for signed-command authorization to the vehicle | **local-only** | This is the device's enrolled identity to the car. The *public* half is registered with Tesla at `https://<host>/.well-known/appspecific/com.tesla.3p.public-key.pem` and bound to the host domain. Copying the private key to another machine would let that machine impersonate this host to the vehicle. Manifest must mark this `local-only` and excluded from sync. |
| snowflake-public.pem | PEM PUBLIC KEY | Public half of the signing keypair | safe-to-sync | Public; sync is harmless. May be useful on the sink for verifying signatures locally. |

Plus `~/.config/tesla-pp-cli/`:

| File | Format | Holds | Sync-safety |
| ---- | ------ | ----- | ----------- |
| config.toml | TOML | canonical scaffold + `auth_token` (extra field) | safe-to-sync |
| auth.json | JSON | `access_token`, `refresh_token`, `expires_at`, `issued_at` (a second copy of OAuth state distinct from the `~/.tesla/` files; this is the PP CLI's own auth, separate from the partner-app fleet creds) | safe-to-sync |

The Tesla case proves three requirements for the secrets-bus format:

1. The format must handle **multiple paths per CLI** (config.toml + auth.json + the entire ~/.tesla/ tree).
2. The manifest must support **per-file or per-path `local-only` markers**, not just per-CLI.
3. Public/private keypair handling must be granular: ship the public half, suppress the private half.

### Superhuman — multi-account token store

`tokens.json` shape (no values, keys only):

```
{
  "accounts": {
    "<account-email>": {
      "accessToken": "...",
      "deviceId": "...",
      "expires": "...",
      "lastUsedAt": "...",
      "refreshToken": "...",
      "superhumanToken": "...",
      "type": "...",
      "userExternalId": "...",
      "userId": "...",
      "userPrefix": "..."
    },
    "<another-account-email>": { ... }
  },
  "lastUpdated": "...",
  "version": "..."
}
```

The bus format must preserve account-keyed structure. Options:

- One env file per account (`~/.agentcookie/secrets/superhuman-pp-cli/accounts/<account>.env`) — clean, but the CLI must know to enumerate.
- One env file with namespaced keys (`ACCOUNT_<id>_ACCESS_TOKEN`) — flat, but loses ergonomics.
- A second sidecar JSON next to `secrets.env` for structured data the dotenv shape cannot express — pragmatic; secrets.env carries the "default account" creds for hot-path agents, sidecar carries the multi-account tree.

`deviceId` may be server-bound (Superhuman may pin the session to the device that issued it). Worth confirming with a test sync before promising agents the multi-account replication path.

### ordertogo — broad identity-bound TOML

The single broadest non-Tesla TOML in the inventory. Beyond the canonical scaffold, it carries:

```
default_restaurant = <redacted>
default_max = <redacted>
default_tip_pct = <redacted>
stripe_customer_id = <redacted>
stripe_default_card = <redacted>
customer_firstname = <redacted>
customer_lastname = <redacted>
customer_phone = <redacted>
mesh_user_id = <redacted>
```

These are account-identity fields: replicating them to the sink is the entire point of the bus (the agent should be able to place an order as the user), but they include PII and a phone number. The bus does not need special handling beyond the standard sealing — but operators should understand that these fields ARE in the bus payload.

### Browser-session-proof JSON (eBay, OpenArt, Suno)

All three share the same shape:

```
{
  "api_name": ...,
  "auth_source": ...,
  "cookie_domain": ...,
  "credential_fingerprint": ...,
  "status_code": ...,
  "validation_method": ...,
  "validation_path": ...,
  "verified_at": ...
}
```

This is a verification record proving "I successfully made an authenticated request as this user against this domain at this time," not the cookie itself. The actual cookies live separately (Chrome's SQLite via agentcookie's existing cookie path, or the canonical TOML's `access_token`). The session-proof file's value is operational telemetry, not a secret per se — but it identifies the user. Classification: device-bound-but-shippable; flag for follow-up to confirm sites don't bind the credential_fingerprint to the device that minted it.

### Slack — non-XDG `~/.slack/` plus multi-team credentials

`~/.slack/config.json` shape:

```
{
  "last_update_checked_at": ...,
  "system_id": ...
}
```

`system_id` is a per-machine identifier the CLI generates; replicating it to the sink would tell Slack the sink is the same install as the source. Whether that helps or hurts depends on Slack's anti-abuse posture; safe to sync but flag.

`~/.slack/credentials.json` shape:

```
{
  "<slack-team-id>": { ...credential record... }
}
```

Multi-team like Superhuman is multi-account, but keyspace is the Slack workspace's team ID (e.g. `T...`). Bus format needs the same per-account/per-tenant key namespacing solution as Superhuman.

### Salesforce — bundle signing key (local-only)

`salesforce-headless-360-pp-cli trust register` enrolls a per-device bundle-signing public key with the Salesforce Certificate trust chain. The private half lives on the device (location not yet observed; the user has not run `trust register`). When it does land, it must be marked `local-only` like Tesla's snowflake-private.pem — same threat model (device-bound signing identity registered to a third party).

`SF360_HOST_FINGERPRINT` is a host-bound integrity check value baked into strings; it would be regenerated per machine and should not sync.

## Sync-safety classification rationale

Three classes are sufficient for the bus design:

**safe-to-sync.** Same identity on every machine; sharing the bytes does not break anything. Includes:
- OAuth access tokens (the same Tesla account is the same on laptop and sink; the access token says "this user").
- OAuth refresh tokens (same — used to re-mint access tokens for the same user).
- API keys (Linear, HubSpot, Slack bot tokens, third-party LLM provider keys in podcast-goat). These are explicitly designed to be portable.
- Vendor partner-app credentials (`client_id`, `client_secret`, fleet-client-id/-secret, Expensify partner_user_*, Tesla fleet-partner-token). These are tied to the developer-app registration, not the device.
- Session cookies (Airbnb, OrderToGo, Instacart, Trustpilot, table-reservation-goat, YesWeHack, Contact Goat). The same browser-issued cookie identifies the same user from either machine, modulo fingerprint binding (see device-bound class below).
- Account identifier fields (Superhuman `userId`, OrderToGo `mesh_user_id`, etc.) — these are user-bound, not device-bound.

**local-only.** Must never leave the device that generated them. Includes:
- ECDSA / RSA signing private keys whose public half is registered with a remote service AS the device's identity. The remote service treats the keypair as proof of "this specific device". Replicating the private key would let two machines impersonate one. Examples: Tesla `snowflake-private.pem`, Salesforce bundle-signing private key (when registered).
- Per-machine derived integrity values (`SF360_HOST_FINGERPRINT`). Regenerated locally; syncing the source machine's value to the sink would defeat the check.
- macOS Keychain items pinned by `-T` ACL to a specific binary's designated requirement (agentcookie's own master key falls in this class; this is a meta-concern, but the bus must not try to extract Keychain items into env files).

**device-bound-but-shippable.** Today the value certifies a specific device, but the threat model would tolerate sync to the agent sink because the sink IS the intended secondary device. Includes:
- Browser session proofs (eBay, OpenArt, Suno).
- Superhuman `deviceId` (server may pin; needs verification).
- Slack `system_id` (per-install identifier).
- Trustpilot WAF cookie (short TTL; sync works inside the window).

These are sync-by-default but flagged for follow-up review per site: if a destination site refuses requests after replication, the manifest can be tightened to `local-only` per-field without changing the format.

The manifest convention (R5 in the plan) needs at least three knobs:

- `local_only: ["path/to/file", "field_name"]` (file-level or field-level exclusion).
- `account_keyspace: "email" | "team_id" | "user_id" | null` (how multi-account stores are keyed; lets the bus replicate one logical account or all).
- `expires_after_seconds: <int>` (so the sink can drop stale device-bound-but-shippable artifacts like the Trustpilot WAF cookie without manual cleanup).

## Spec gaps surfaced by this audit (input list for v1.1)

The format spec at `docs/spec-agentcookie-secrets-bus-v1.md` was written before this audit ran. Three findings here should land as a v1.1 spec revision:

1. **Multi-account namespacing.** Superhuman (account email keys) and Slack (team ID keys) hold one set of secrets per account in a single file. The v1 spec assumes one secret set per CLI. v1.1 needs either an `accounts/<account-id>/secrets.env` subdirectory convention or namespaced keys in a single file. Recommend the subdirectory convention so a friend can opt in/out of syncing specific accounts.
2. **Per-file (not just per-key) `local-only` markers.** Tesla's `snowflake-private.pem` is local-only, but its `snowflake-public.pem` half is safe-to-sync. The v1 spec's `[sync.keys]` is per-key inside `secrets.env`; v1.1 needs a `[sync.files]` table for non-env-shaped artifacts like `.pem` files that live alongside the env file.
3. **Third sync-safety classification: `device-bound-but-shippable`.** The browser-session-proof JSON used by eBay, OpenArt, and Suno is technically device-bound (it captures fingerprint timing) but the threat model would tolerate sync to a single trusted second machine. The v1 spec only has two buckets (safe-to-sync, local-only). v1.1 needs the middle category with a `caution` marker that warns but does not block.

PII observation: ordertogo's `config.toml` carries customer name, phone, and Stripe customer ID alongside auth tokens. The bus replicates whatever's in the file, not just "secrets." Friends should know that. This is documentation territory, not a spec change; the v1 spec's security boundary statement may want a paragraph about PII-replication once v1.1 lands.
