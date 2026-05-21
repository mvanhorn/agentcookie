---
title: "feat: Headless sink (skip Chrome SQLite write + CDP injection)"
status: active
type: feat
created: 2026-05-21
target_release: v0.12.0-beta.3
---

# feat: Headless sink (skip Chrome SQLite write + CDP injection)

## Problem Frame

A friend installing agentcookie on a headless Mac mini hits one unavoidable interaction: the Chrome Safe Storage Keychain prompt. The sink daemon needs Chrome's per-machine AES-128 cookie key to encrypt cookies before writing Chrome's SQLite. macOS only grants that access via a GUI "Always Allow" click, and SSH-only installs have no one to click it. This was the dominant blocker in the 2026-05-19 first-friend dry-run (friction items #11, #18) and remains the only setup step v0.12.0-beta.2 cannot eliminate.

The architectural opening: agentcookie already ships two cookie-delivery paths that do NOT need Chrome Safe Storage — the v0.8 sidecar (`~/.agentcookie/cookies-plain.db`, encrypted with the pair-derived shared key, read by cookiesource-aware callers via env var) and the v0.11 adapter push (writes each PP CLI's session cache file directly). The Chrome SQLite write path is the only consumer that requires Chrome's key. Drop that write on headless installs and the Keychain dependency disappears.

Phase 2 restores the "open Chrome on the sink and see synced cookies" affordance click-free by having agentcookie launch its own Chrome via CDP and inject cookies through `Storage.setCookies`. Chrome handles its own Safe Storage; agentcookie never touches it.

## Summary

Two-phase change. Phase 1 adds a `skip_chrome_sqlite` mode to the sink: when set, the daemon never reads Chrome Safe Storage, never writes Chrome SQLite/leveldb. Sidecar + adapter push remain unchanged and continue to serve PP CLIs. Wizard install auto-detects no-TTY contexts (the headless SSH install path) and writes `skip_chrome_sqlite: true` into `sink.yaml`. GUI installs are unchanged. `agentcookie doctor` surfaces the mode and flags configurations where neither sidecar nor an adapter would serve a given cookie domain.

Phase 2 adds chromedp as a dependency, fixes the Chrome 127+ App-Bound Encryption prefix strip on the CDP path (open issue #10), and wires a one-shot-launch-per-sync CDP injection mode. Each sync, the sink spawns a fresh chromedp instance against the agentcookie-owned profile at `~/.agentcookie/chrome-profile/`, calls `Storage.setCookies` with the synced cookies, and exits. Chrome encrypts its own SQLite with its own Safe Storage key; agentcookie never reads Chrome's Keychain item.

Both phases ship as v0.12.0-beta.3 in a single combined release.

## Requirements

- R1. Sink can run on a fully headless Mac mini (SSH install, no monitor, no Screen Sharing) without a Chrome Safe Storage Keychain prompt blocking startup.
- R2. PP CLIs continue to receive synced cookies on a `skip_chrome_sqlite` sink, via sidecar and adapter push, with no PP-CLI code changes.
- R3. `agentcookie wizard install --as sink` on an SSH session (no TTY on stdin) writes `skip_chrome_sqlite: true` into `sink.yaml`. GUI installs keep the existing default (write Chrome SQLite).
- R4. `agentcookie doctor` reports the active mode and warns when a configured peer has cookie domains that no adapter and no sidecar reader would cover (degraded mode visibility).
- R5. When `cdp.enabled: true` (Phase 2), the sink launches headless Chrome per sync, injects cookies via `Storage.setCookies`, and exits. The injection strips the 32-byte App-Bound prefix from cookie values before the CDP call.
- R6. Existing v0.12 installs upgrading to v0.12.0-beta.3 keep their current behavior — absence of `skip_chrome_sqlite` in `sink.yaml` continues to write Chrome SQLite as today. No silent behavior change for installed friends.

## Scope Boundaries

In scope:
- Sink runtime mode flag, wizard auto-detection, doctor visibility.
- chromedp dependency + CDP injector with App-Bound prefix strip.
- Single combined v0.12.0-beta.3 release covering both phases.
- Quickstart docs update for the headless flow.

### Deferred to Follow-Up Work

- Phase 3: "drive Chrome to refresh URLs" loop after each CDP inject — keeps server-side sessions warm by simulating user visits. Risk/reward unclear (anti-bot exposure, account-security alerts).
- Continuous Chrome mode (`cdp.mode: continuous`) — keep Chrome up between syncs for lower per-sync latency. One-shot is sufficient for v0.12.0-beta.3.
- Migration helper for existing installs to opt into `skip_chrome_sqlite` post-install — friends with running sinks who want to switch can manually edit `sink.yaml` and restart.
- Linux/Windows sink support — still macOS-only.

## Key Technical Decisions

- **`skip_chrome_sqlite` is sink-side runtime config, not a CLI flag.** The mode persists in `sink.yaml`, set once at wizard time. A flag would require operators to update LaunchAgent invocations; a config field is read on every sink start.
- **Auto-detect via stdin TTY at wizard time.** `wizard install --as sink` checks `terminal.IsTerminal(int(os.Stdin.Fd()))`. No TTY → headless context → default `skip_chrome_sqlite: true`. Operators on a GUI Terminal at the sink get the legacy default. Either side can override via explicit `--skip-chrome-sqlite` / `--write-chrome-sqlite` flags on wizard install.
- **chromedp, not raw CDP.** chromedp wraps the websocket protocol, lifecycle, and headless-mode quirks. Adding a non-trivial new dep is acceptable here because writing it ourselves would reinvent ~2K lines of well-tested code (cdp navigation, target attach, frame routing).
- **One-shot per sync.** Sink spawns Chrome, sets cookies, exits. Steady-state RAM stays near zero. Per-sync latency ~1-2s for Chrome startup is acceptable (syncs aren't user-facing); avoids Chrome-lifecycle complexity (crash recovery, file-lock contention, persistent debug-port management).
- **Reuse the existing `~/.agentcookie/chrome-profile/` dir.** Created by v0.11 architecture; agentcookie-owned, not the friend's default Chrome profile. CDP injection targets this profile via `--user-data-dir`. The friend's default Chrome profile is untouched.
- **App-Bound prefix strip lives in `internal/cdp/setcookies.go`** (new file). The SQLite path in `internal/chrome/cookies.go` is unaffected — its cookies are written through Chrome's own decrypt-then-re-encrypt cycle, which transparently handles the prefix. Only the CDP path needs the strip per the memory at `reference_chrome_app_bound_encryption.md`.
- **No new fields on the wire protocol.** Sink-side modes are local config. Source pushes the same envelope shape regardless of how the sink writes.

## High-Level Technical Design

Sink runtime flow with both modes wired in (illustrative — not implementation specification):

```
agentcookie sink (cmd start)
  ├─ Load sink.yaml
  ├─ if skip_chrome_sqlite { skip chrome.SafeStoragePassword() }
  │   else { read Chrome Safe Storage key (existing behavior) }
  ├─ Bind listener on 100.x.y.z:9999
  └─ on POST /sync:
       ├─ Decrypt envelope, run blocklist filter
       ├─ Write sidecar (always)
       ├─ if cdp.enabled { launch chromedp, Storage.setCookies (one-shot), close }
       ├─ if !skip_chrome_sqlite { applyEnvelopeToSink (existing SQLite + leveldb + indexeddb path) }
       └─ Always: sinkpush.RunAll (adapter push, existing)
```

`sink.yaml` shape after Phase 2:

```yaml
listen:
  addr: 100.80.229.80:9999
chrome:
  db_path: ~/Library/Application Support/Google/Chrome/Default/Cookies
peer:
  hostname: macbook-pro-44
skip_chrome_sqlite: true          # NEW (Phase 1) — when true, sink never reads Chrome Safe Storage
cdp:                              # NEW (Phase 2) — opt-in CDP injection
  enabled: false
  profile_dir: ~/.agentcookie/chrome-profile
```

## Implementation Units

### U1. Add `skip_chrome_sqlite` config + sink runtime branch

**Goal:** Sink can run end-to-end without reading Chrome Safe Storage when configured.

**Requirements:** R1, R2, R6.

**Dependencies:** none.

**Files:**
- `internal/config/config.go` — add `SkipChromeSQLite bool yaml:"skip_chrome_sqlite,omitempty"` to `SinkConfig`.
- `internal/config/config_test.go` — round-trip + default-false test.
- `internal/cli/sink.go` — gate the `chrome.SafeStoragePassword()` call AND the `applyEnvelopeToSink` Chrome SQLite write on `!cfg.SkipChromeSQLite`. Keep sidecar + `sinkpush.RunAll` always running.
- `internal/state/sink.go` — extend `SinkState.LastWriteMode` value set to include `"sidecar+adapter"` for the skip-sqlite path (existing values: `"sqlite+leveldb"`, `"dry-run"`).
- `internal/cli/sink_test.go` — new test exercising the skip-sqlite branch: sink boots, accepts a /sync, writes sidecar, runs adapters, never calls Chrome Safe Storage.

**Approach:** The two early calls in `runSink` — `chrome.SafeStoragePassword()` and `chrome.DeriveAESKey()` — become conditional. When `SkipChromeSQLite` is set, the AES `key` is left nil. Downstream, `applyEnvelopeToSink` only fires when `key != nil` (which already happens to be the SQLite write path). The sidecar write inside `applyEnvelopeToSink` needs to relocate so it runs in both modes; alternatively, factor out a `writeSidecarAndAdapters(envelope, cookies)` helper called from both branches. Preference: factor out the helper to keep the control flow obvious.

**Patterns to follow:** the existing `sinkDryRun` flag and its branching at sink.go:69-83. The new flag mirrors that shape but instead of stub-dumping to stderr, it does real sidecar + adapter writes.

**Test scenarios:**
- happy path: `skip_chrome_sqlite: true` in config, sink starts, no Chrome Safe Storage read attempted, listener binds, `/sync` accepts a payload, sidecar gets the cookies, adapter push fires.
- happy path: `skip_chrome_sqlite: false` (or absent) keeps the existing SQLite+leveldb write path verbatim. Regression guard for installed friends per R6.
- edge case: config field is absent in `sink.yaml` — defaults to false, matches v0.12.0-beta.2 behavior.
- error path: chrome.SafeStoragePassword is mock-failed in test; with `skip_chrome_sqlite: true` the sink still boots; with `skip_chrome_sqlite: false` it errors out as today (covers the existing `read Chrome Safe Storage from Keychain` error path is not regressed).
- integration: `state/sink.go` writes `LastWriteMode: "sidecar+adapter"` in skip mode (visible to `doctor`).

**Verification:** sink starts on a machine where Chrome Safe Storage access returns errSecAuthFailed when `skip_chrome_sqlite: true`. `agentcookie doctor --json` shows the new write mode in the sink-state check.

---

### U2. Wizard auto-detect headless context

**Goal:** `wizard install --as sink` over SSH (no TTY) writes `skip_chrome_sqlite: true`; GUI installs keep the legacy default.

**Requirements:** R3, R6.

**Dependencies:** U1.

**Files:**
- `internal/cli/wizard.go` — extend `wizardInstallSink` to detect `!isatty(stdin)` and pass through to `renderSinkYAML`. Add explicit `--skip-chrome-sqlite` / `--write-chrome-sqlite` flag pair to override the auto-detect. Auto-detect default also implies `--skip-keychain-prompt` (already implied today; keep the symmetry).
- `internal/cli/wizard.go` `renderSinkYAML(peer, listenAddr, skipChromeSQLite)` signature update — emit the line conditionally so `false` cases keep the existing YAML shape (backward compat).
- `internal/cli/wizard_test.go` — new TestRenderSinkYAML_SkipChromeSQLite covering true/false.
- `scripts/install-beta.sh` — when auto-detect already adds `--skip-keychain-prompt` (post-beta.2), also emit the post-install hint mentioning the new mode and what it means for sink Chrome browser.
- `docs/quickstart-beta.md` — add a "What headless mode means" paragraph to the existing "Headless sink" section.

**Approach:** Add a helper `isHeadlessInstall()` returning `true` when stdin is not a TTY. The sink branch reads that helper exactly once, lets explicit flags win. Render path becomes:

```
skip := wizardSkipChromeSQLite || (isHeadlessInstall() && !wizardWriteChromeSQLite)
writeYAMLIfMissing(sinkYAMLPath, renderSinkYAML(wizardPeer, listenAddr, skip), wizardForce)
```

**Patterns to follow:** the install-beta.sh `--skip-keychain-prompt` auto-add at scripts/install-beta.sh:185-198 — same shape, same detection signal.

**Test scenarios:**
- happy path: no-TTY install path emits `skip_chrome_sqlite: true` in the rendered YAML.
- happy path: TTY install path (golang.org/x/term IsTerminal returns true in test via a fake fd) emits the YAML without the field, matching v0.12.0-beta.2 output byte-for-byte (R6 regression guard).
- edge case: explicit `--write-chrome-sqlite` overrides headless detection.
- edge case: explicit `--skip-chrome-sqlite` overrides GUI default.

**Verification:** running `wizard install --as sink ...` from this laptop's SSH session into a fresh Mac mini produces a `sink.yaml` containing `skip_chrome_sqlite: true`; the same wizard run on the Mac mini's local Terminal does not.

---

### U3. `agentcookie doctor` surfaces the mode and gaps

**Goal:** `doctor` reports the active write mode and warns when configured cookie domains aren't covered by any adapter or sidecar reader.

**Requirements:** R4.

**Dependencies:** U1.

**Files:**
- `internal/cli/doctor.go` — extend the existing "Sink state" check to report `last_write_mode`. Add a new check "Adapter coverage" that loads the sidecar (when present) and the adapter registry, and reports OK if every cookie host_key in the sidecar has at least one of (sidecar-readable: yes by definition, OR matching adapter in `sinkpush.Registry()`). WARN (not FAIL) when domains are uncovered, listing the top 3.
- `internal/cli/doctor_test.go` — extend test envelope to cover both modes + the new coverage check.

**Approach:** The doctor check is best-effort visibility, not enforcement. The sidecar by definition covers everything in it for sidecar-aware callers, so the check reduces to: "for each unique host_key in sidecar, is there an adapter AND does the PP CLI for that adapter exist on this Mac?" If neither sidecar-readable callers nor an adapter exist for a domain, surface a WARN with remediation pointer (link to the adapter list + sidecar env var doc).

**Patterns to follow:** the existing doctor checks at doctor.go (Tailscale, config, keystore, sink listener) — same shape (struct, severity, message + remediation pointer, JSON envelope entry).

**Test scenarios:**
- happy path: sidecar has cookies for instacart.com, all-green (adapter exists, PP CLI installed).
- happy path: sidecar has cookies for example.com, no adapter — WARN with remediation pointer.
- happy path: `skip_chrome_sqlite: false` → check reports "Write mode: chrome-sqlite+sidecar+adapter (legacy)".
- happy path: `skip_chrome_sqlite: true` → "Write mode: sidecar+adapter (headless)".
- edge case: sidecar absent (fresh install, no syncs yet) — check reports SKIPPED with reason.
- integration: full `doctor --json` envelope still has its 8 entries when both new conditions are present; the coverage entry is INFO when sidecar absent.

**Verification:** `agentcookie doctor` on the existing Mac mini sink (which writes both Chrome SQLite and sidecar) reports the legacy mode; flipping `skip_chrome_sqlite: true` and restarting flips the reported mode to headless. The coverage check surfaces the 8 of so cookie domains currently in sidecar.

---

### U4. Add chromedp + CDP injector with App-Bound prefix strip

**Goal:** New `internal/cdp/` package can take a list of cookies and inject them into a running Chrome via `Storage.setCookies`, stripping the 32-byte App-Bound prefix from each value first.

**Requirements:** R5.

**Dependencies:** none (does not depend on U1-U3 wiring).

**Files:**
- `go.mod`, `go.sum` — add `github.com/chromedp/chromedp` (pin to a current release tag, document the version choice in commit body). Run `go mod tidy`.
- `internal/cdp/setcookies.go` — new file. Exports `InjectCookies(ctx context.Context, profileDir string, cookies []protocol.Cookie) error` that spawns chromedp with `chromedp.UserDataDir(profileDir)` + headless + `--remote-debugging-port=0`, builds `network.SetCookiesParams` from the input, strips the App-Bound prefix from each `Value`, invokes the command, returns.
- `internal/cdp/prefix.go` — new file. Exports `StripAppBoundPrefix(value []byte) []byte` per the memory documentation. Tested standalone.
- `internal/cdp/prefix_test.go` — unit tests for the prefix strip (32-byte prefix present, prefix absent, value shorter than 32 bytes → return as-is, multiple cookies sharing the same host prefix → strip same offset).
- `internal/cdp/setcookies_test.go` — integration test that requires Chrome at build/test time. Gate behind a build tag (`//go:build chrome`) so CI without Chrome can still run.

**Approach:** The prefix strip is the load-bearing detail. Per `~/.claude/projects/-Users-mvanhorn/memory/reference_chrome_app_bound_encryption.md`: decrypted v10 plaintext is `<32-byte-host-bound-prefix> || <actual-cookie-value>` when sourced from Chrome 127+. The CDP path needs the raw plaintext value (the second segment) as a UTF-8 string; passing the prefixed bytes embeds Chrome's internal format inside the cookie value and the destination site rejects it as malformed. The strip is unconditional when input length ≥ 32 — the leading 32 bytes have a recognizable structure per the memory (identical across cookies sharing a host_key), but we don't need to parse it, just drop it. For inputs <32 bytes, return as-is (no v10 prefix — likely a v11 or legacy cookie).

**Patterns to follow:** the chromedp idiom of `chromedp.Run(ctx, network.SetCookies(params))`. See chromedp's own examples for the headless + user-data-dir spawn shape.

**Test scenarios:**
- happy path: 32-byte prefix followed by ASCII value "abc123" → returns "abc123".
- happy path: 32-byte prefix followed by binary value (containing null bytes) → returns binary value verbatim.
- edge case: value is exactly 32 bytes → returns empty (this is a v10 cookie with no actual value, which is valid in Chrome's format).
- edge case: value is 16 bytes (legacy or v11 cookie) → returns as-is, no strip.
- edge case: value is empty → returns empty.
- edge case: multiple cookies for the same host_key → all use the same 32-byte prefix offset; strip is idempotent per-cookie.
- integration (chrome build tag): InjectCookies spawns chromedp against a temp profile dir, injects 5 cookies for github.com, asserts each cookie is queryable via `Network.GetAllCookies` with the post-strip value.

**Verification:** Running InjectCookies against a real Chrome profile (test) results in cookies being readable by a subsequent Chrome launch pointed at the same profile — the cookies show up in chrome://settings/cookies with the correct decoded values.

---

### U5. Wire CDP injector into sink one-shot-per-sync

**Goal:** When `cdp.enabled: true` in `sink.yaml`, every `/sync` triggers a CDP injection after the sidecar write.

**Requirements:** R1, R5.

**Dependencies:** U1, U4.

**Files:**
- `internal/config/config.go` — add `CDP CDPRef yaml:"cdp,omitempty"` to `SinkConfig`. `CDPRef { Enabled bool; ProfileDir string }`.
- `internal/config/config_test.go` — round-trip + default-disabled test.
- `internal/cli/sink.go` — inside the `/sync` handler, after sidecar write, when `cfg.CDP.Enabled`, call `cdp.InjectCookies(ctx, profileDir, cookies)`. Log result; failure does not fail the /sync response (sidecar write was successful). Update `sinkState.LastWriteMode` to include `+cdp` when CDP fired this sync.
- `internal/cli/sink_test.go` — new test exercises CDP-enabled sync path with a stub injector (interface), asserts the stub was called with the right cookie set.

**Approach:** Introduce a small interface `cdp.Injector` so the production path uses `cdp.InjectCookies` and tests inject a stub. The wiring is a single conditional block inside the existing `/sync` handler. Failure mode: CDP errors are logged but do not error the /sync response — sidecar is the authoritative path, CDP is best-effort.

**Patterns to follow:** the existing `sinkpush.RunAll(cookies)` block at sink.go:206 — same shape (run after sidecar, log results, do not bubble errors to the /sync response).

**Test scenarios:**
- happy path: `cdp.enabled: true` → injector is called once per sync, with the same cookies list that hit sidecar.
- happy path: `cdp.enabled: false` (or absent) → injector is not called.
- error path: injector returns error → sink logs the error, /sync response is still 200 OK (sidecar + adapter pushes succeeded).
- edge case: `cdp.profile_dir` is empty → default to `~/.agentcookie/chrome-profile`.
- integration: combined with U1, a `skip_chrome_sqlite: true` + `cdp.enabled: true` sink produces a sidecar write + CDP inject per sync, never reads Chrome Safe Storage.

**Verification:** Live test on the Mac mini (post-wizard install with CDP enabled): source pushes 1 sync, sink writes sidecar, spawns chromedp, injects cookies, exits. `agentcookie doctor` reports `LastWriteMode: "sidecar+adapter+cdp"`. Opening the agentcookie-owned Chrome profile (via `Chrome.app --user-data-dir=~/.agentcookie/chrome-profile`) shows the synced cookies present and decryptable.

---

### U6. Wizard install integrates CDP option + doctor reports CDP status

**Goal:** Wizard install offers CDP-enable when `skip_chrome_sqlite: true`, and `doctor` reports CDP status in the same envelope.

**Requirements:** R4, R5.

**Dependencies:** U2, U3, U5.

**Files:**
- `internal/cli/wizard.go` — when sink wizard runs with `skip_chrome_sqlite: true` (auto-detected or explicit), add `cdp.enabled: true` + `cdp.profile_dir: ~/.agentcookie/chrome-profile` to the rendered `sink.yaml`. Add `--no-cdp` flag to opt out. Ensure `~/.agentcookie/chrome-profile/` directory exists (mkdir).
- `internal/cli/wizard.go` `renderSinkYAML(...)` — extend to emit the `cdp:` block when enabled.
- `internal/cli/doctor.go` — new check "CDP injector": verifies `cdp.profile_dir` exists and is writable, OR reports SKIPPED when `cdp.enabled: false`.
- `internal/cli/wizard_test.go` — extend rendering tests to cover both modes.
- `internal/cli/doctor_test.go` — extend with the new CDP check.

**Approach:** The wizard treats CDP as the "complement" feature for headless mode. The default is: headless install → both `skip_chrome_sqlite: true` AND `cdp.enabled: true`. Operators who want the bare sidecar+adapter mode (no Chrome on sink at all) pass `--no-cdp`. The doctor check is best-effort: it doesn't actually run chromedp (that takes seconds), just verifies the profile dir exists and the chromedp binary is in PATH (via `exec.LookPath("google-chrome")` or fallback to checking `/Applications/Google Chrome.app`).

**Patterns to follow:** the existing doctor "Sink listener" check — same shape, same JSON envelope contribution.

**Test scenarios:**
- happy path: headless wizard install (no TTY) → both flags set, profile dir created, doctor reports OK.
- happy path: `--no-cdp` on a headless install → `skip_chrome_sqlite: true` only, `cdp.enabled` absent, doctor reports SKIPPED.
- edge case: profile dir already exists from v0.11 → mkdir is idempotent.
- edge case: Chrome.app not installed on sink → doctor WARN (CDP injection will fail at sync time), include remediation pointer.

**Verification:** A fresh headless wizard install on a clean sink produces a sink.yaml with both new sections; `agentcookie doctor` runs green; a manually triggered sync (`agentcookie source --once` on this laptop) results in Chrome cookies present in the agentcookie-owned profile on the sink.

---

### U7. End-to-end dry-run + cut v0.12.0-beta.3

**Goal:** Validate the full headless install flow end-to-end on the existing Mac mini, then publish v0.12.0-beta.3 prerelease.

**Requirements:** R1, R2, R3.

**Dependencies:** U1-U6.

**Files:**
- `CHANGELOG.md` — new v0.12.0-beta.3 section documenting the headless mode + CDP injection.
- `docs/quickstart-beta.md` — already updated by U2 (headless section); add the "Sink Chrome affordance via CDP" subsection.
- `docs/dry-run-2026-05-21.md` — second dry-run friction log (parallel to dry-run-2026-05-19.md).

**Approach:** Reset Mac mini sink to clean state (same reset flow as 2026-05-19 dry-run: `launchctl bootout`, wipe `~/.agentcookie`, remove `~/.config/agentcookie`, remove binary). Install over SSH using v0.12.0-beta.3 release artifact via `install-beta.sh`. Verify all auto-detected defaults take effect. Verify sync end-to-end without any GUI interaction. Capture friction items. Cut release after dry-run passes.

**Execution note:** This unit is the validation gate. If the dry-run surfaces blockers, file a follow-up plan rather than patching this plan; v0.12.0-beta.3 ships when this unit's verification passes.

**Test scenarios:**
- happy path (manual): SSH install on freshly-wiped Mac mini, friend runs `install-beta.sh --as sink --peer macbook-pro-44 --code <code> --pair-url <url>`. Zero GUI prompts. Sync succeeds within 30 seconds. `agentcookie doctor` reports green.
- happy path (manual): PP CLI (`instacart-pp-cli carts`) over SSH succeeds without auth login.
- happy path (manual): launching Chrome.app on the Mac mini against the agentcookie-owned profile shows synced cookies present (CDP injection round-trips through Chrome's own SQLite).
- regression: existing v0.12.0-beta.2 sink upgraded in place (binary swap, no config changes) keeps working in legacy mode (no behavior change for existing friends).

**Verification:** `gh release view v0.12.0-beta.3 --json tagName,isPrerelease,assets` returns the published release. `docs/dry-run-2026-05-21.md` is committed with friction items (if any) and a clear go/no-go for friend invites.

---

## System-Wide Impact

- **PP CLIs** are not modified. Sidecar + adapter push are the existing v0.8/v0.11 paths; both already serve cookies without depending on Chrome SQLite write. Five built-in adapters (instacart, airbnb, ebay, pagliacci, table-reservation) plus any kooky-using CLI that honors `AGENTCOOKIE_PLAIN_COOKIES`. No PP CLI release coordination needed.
- **Source side** is unchanged. The sync protocol envelope is identical; source doesn't know or care how the sink writes.
- **Existing v0.12.0-beta.2 installs** keep their behavior on binary upgrade (R6 regression guard). The new mode is opt-in via fresh-install wizard auto-detection or manual `sink.yaml` edit. No silent breakage.
- **Friend onboarding** changes meaningfully — the "you'll need to Screen Share to grant Keychain access" caveat disappears for the headless cohort. Quickstart docs update to reflect the new no-click flow.
- **CI/release** — chromedp adds a substantial Go module to the dep tree (~50K LOC vendored). Test runtime gains chromedp's unit tests; CI binary build size is unchanged (chromedp pulls in transitive deps but most are also already present).

## Risks and Mitigations

- **chromedp instability across Chrome versions.** Chrome 127+ App-Bound work continues to evolve; future Chrome releases may introduce new CDP behaviors that break injection. Mitigation: pin chromedp to a release that's tested against Chrome 130+ (current as of 2026); track chromedp updates as part of normal dep maintenance; the App-Bound prefix strip is well-documented per our memory and unlikely to change shape without a Chrome version bump that we'll see in advance.
- **CDP injection failure under sink load.** Spawning Chrome per sync (every few minutes during active source browsing) could thrash the sink. Mitigation: one-shot Chrome adds ~1-2s per sync — acceptable; if sync frequency increases above 1/min in practice, revisit continuous mode.
- **Adapter coverage gaps remain invisible to friends.** A friend syncing a domain with no adapter and no sidecar reader silently gets nothing. Mitigation: U3 doctor coverage check surfaces this; quickstart explicitly calls out the fallback flow (sidecar env var for ad-hoc PP CLIs).
- **Chrome.app must be installed on the sink.** CDP can't inject without it. Mitigation: doctor warns when not detected (U6); install-beta.sh already checks for Chrome at prereqs and warns when absent (existing behavior, no change).
- **One-shot Chrome leaves cookies "behind" only when Chrome itself writes back.** If the friend's normal Chrome.app on the sink is launched against a different profile, those cookies stay in the agentcookie profile — invisible from the friend's default Chrome. Mitigation: doctor reports the profile dir; quickstart documents launching Chrome with `--user-data-dir` to inspect.

## Acceptance Criteria

- Fresh SSH install on a wiped Mac mini sink completes end-to-end without any GUI interaction.
- `agentcookie doctor` reports green; LastWriteMode shows `sidecar+adapter+cdp`.
- An existing v0.12.0-beta.2 friend's sink, upgraded by replacing only the binary, continues to work unchanged.
- `instacart-pp-cli carts` over SSH returns the friend's logged-in cart state.
- v0.12.0-beta.3 prerelease is published with notarized binary, signed tarball, updated release notes, and an attached docs/dry-run-2026-05-21.md committed to main.

## Deferred Questions

None blocking. Outstanding follow-ups (Phase 3 territory):
- Should the sink drive Chrome to navigate to each cookie's domain after CDP inject, to fire the refresh-token flow server-side? Pros and cons explored in conversation; risk/reward unclear; defer until v0.12.0-beta.3 has friend feedback.
- Should `cdp.mode: continuous` be added later? Defer until per-sync latency feels limiting in real use.

## Origin

This plan was generated from a planning session on 2026-05-21 following the 2026-05-19 first-friend dry-run (docs/dry-run-2026-05-19.md), the v0.12.0-beta.2 ship (release tag), and a design conversation about removing the Chrome Safe Storage Keychain click as the last setup-time interaction. Memory references:
- `reference_chrome_app_bound_encryption.md` — App-Bound prefix details.
- v0.12.0-beta.2 release at https://github.com/mvanhorn/agentcookie/releases/tag/v0.12.0-beta.2.
- Open issue #10 (CDP prefix strip), closed by U4.
