---
artifact_contract: ce-unified-plan/v1
artifact_readiness: implementation-ready
execution: code
product_contract_source: ce-plan-bootstrap
type: feat
created: 2026-09-09
---

# feat: Fan out cookie and secret sync to multiple sinks

## Summary

`agentcookie source` pushes to exactly one sink. `SourceConfig` holds a single `Sink SinkRef` and a single `Peer PeerRef`, and `pushOnce` seals the payload with that one peer's key and POSTs once to `cfg.Sink.URL`. Matt now runs three sink machines (instinct/e2b, a Mac mini, and grok-bot) and can only feed one at a time by editing the URL and re-pushing, which the `--watch` daemon cannot do for more than one target.

This plan adds native multi-sink fan-out. One source config gains a `sinks` list; the read-filter-seal pipeline reads and filters cookies once, then seals per sink with that sink's own peer key and POSTs to each, isolating a failed or unreachable sink so it never blocks the others. The existing single `sink`/`peer` fields stay valid and load as a one-element list, so every deployed config keeps working with no migration. Secrets ride the same sealed payload, so secret fan-out comes for free once the POST loops.

---

## Problem Frame

The push path is single-target by data model, not by accident:

- `internal/config/config.go` — `SourceConfig.Sink` is one `SinkRef{URL}` and `SourceConfig.Peer` is one `PeerRef{Hostname}`. The hostname names a single key under `keys/`.
- `internal/cli/source.go` — `runSource` resolves one transport secret via `resolveTransportSecret(common.ConfigDir, cfg.Peer.Hostname, cfg.Security.SharedSecret)` (third arg is the legacy shared secret string, not the config). `pushOnce` builds the plaintext payload (cookies plus the secrets-bus payload) once, calls `transport.SealWithSecret(payload, secret)`, and POSTs once to `cfg.Sink.URL`.
- `internal/cli/common.go` — `resolveTransportSecret(configDir, peerHost, legacy string)` returns the peer key when present, else falls back to the legacy `Security.SharedSecret`, else errors. This fallback is load-bearing for multi-sink (see KTD2): a missing per-sink key must not silently seal under the fleet-wide shared secret.
- `internal/cli/wizard.go` — pairing files the key under the operator-supplied `--peer` name via `beginSourcePairing` / the pairing-info writer, recording the sink's announced hostname separately. `runPairAsSource` in `pair.go` instead files under `res.RemotePeer` (the announced `os.Hostname`), which is the wrong name for the fan-out lookup (this is the live "announced as e2b.local, stored as instinct" behavior).
- `internal/state/state.go` — `SourceState.SinkURL` is a single string, and the push counters (`LastPush`, `TotalPushes`, `TotalFailures`, `LastError`) are tracked for that one target.

To send to three machines today, the only path is three separate config directories each with its own `source.yaml`, key, and `--watch` LaunchAgent. That works but triples the Chrome reads, seals, and daemons and splits status across three `doctor` runs. Matt confirmed he wants native multi-sink in the repo he owns, with existing single-sink configs left working.

The device-bound (DBSC) cookie caveat is unchanged and per-sink by nature: the ~86 Google cookies pinned to the source Mac will not work on any sink. More sinks does not change that; each sink still needs its own Chrome signed into the same Google account.

---

## Goal Capsule

**Objective:** one `agentcookie source` config keeps N sink machines logged in, so Matt's fleet of sinks all receive cookies and secrets from one watcher without per-target config directories.

**Means:** a `sinks` list in `SourceConfig`, a fan-out push loop that reads once and seals-and-POSTs per sink with per-sink failure isolation, additive wizard pairing to grow the list, and per-sink reporting in `status` and `doctor`.

**Done when:** a config listing three sinks delivers to all three on one push, one dead sink does not fail the others, a legacy single-sink config still works unchanged, and `status`/`doctor` report each sink's last-push and reachability.

---

## Requirements

- **R1** — `SourceConfig` accepts a list of sinks, each carrying its own sink URL and peer hostname (its key).
- **R2** — A legacy `source.yaml` with the single `sink:`/`peer:` fields loads unchanged and behaves exactly as before (one-element list synthesized at load).
- **R3** — One push reads and filters cookies once, then seals per sink with that sink's peer key and POSTs to each sink's URL. Secrets ride the same payload, so each sink receives them too.
- **R4** — A sink that fails to seal or POST (unreachable, auth failure, timeout) is recorded as failed for that sink and does not abort delivery to the other sinks. The push exit status reflects whole-vs-partial success.
- **R5** — The wizard can add a sink to an existing source config additively (pair a new peer and append it) without overwriting the existing sinks.
- **R6** — `status` and `doctor` report per-sink state: URL, last push, last error, and reachability, instead of a single sink.
- **R7** — `--watch` fans out to all configured sinks on each Chrome cookie or secrets write.

---

## Key Technical Decisions

**KTD1. Additive config schema, legacy fields retained** *(session-settled: user-directed — chosen over a hard migration: keep every deployed source.yaml working with no rewrite).* Add `Sinks []SinkTarget` to `SourceConfig`, where `SinkTarget` carries `URL` plus its own `Peer` hostname. Keep the existing `Sink SinkRef` and `Peer PeerRef` fields. At load, when `Sinks` is empty and legacy `Sink.URL` is set, synthesize a one-element `Sinks` list from `Sink`+`Peer`. When `Sinks` is populated, it is authoritative and the legacy scalars are ignored. Marshaling writes `sinks` for multi-sink configs and may leave the legacy fields untouched on configs that still use them. Governs R1, R2.

**KTD2. Read once, seal-and-POST per sink; resolve secret inside the loop** The plaintext payload (cookies after blocklist and DBSC filtering, plus the secrets-bus payload) is built once. The fan-out is only over sealing and transport: for each sink, resolve its secret with `resolveTransportSecret(common.ConfigDir, sink.Peer, cfg.Security.SharedSecret)`, `SealWithSecret` with it, and POST to its URL. Sealing must be per sink because each sink has a distinct pairing-derived key. **Resolve inside the fan-out loop, not up front:** `resolveTransportSecret` hard-errors on a missing key, so resolving all secrets before the loop would let one missing key abort delivery to every sink, defeating R4. A resolution failure is recorded as that sink's failure and the loop continues. **No silent shared-secret downgrade:** a per-sink key that is missing or unreadable must isolate that sink (recorded failure, skipped POST), never fall through to the fleet-wide `Security.SharedSecret` and seal the full payload under it. The shared-secret path stays available only for a legacy single-sink config whose synthesized sink has an empty `Peer` (see KTD1). Governs R3, R4.

**KTD3. Sequential fan-out with a per-sink timeout, failures isolated, `--once` budget scaled** Iterate sinks in order; bound each POST by the existing per-sink `SyncClient` timeout (5 min) so one slow or dead sink cannot stall the rest beyond its own timeout. Collect a per-sink result (ok / error) and continue on error. The push returns success only if every sink succeeded; partial success is reported per sink and reflected in the exit status.

**The `--once` outer deadline must scale with sink count.** `runSource` today wraps the whole push in one `context.WithTimeout(ctx, SyncClient.ClientTimeout + 30s)` (5m30s). Sequential fan-out shares that single budget, so a first sink that runs to its full 5-minute timeout starves sinks 2 and 3 and they fail even when healthy, defeating R4 in `--once` mode. Fix: in `--once`, either scale the outer context to roughly `len(sinks) * ClientTimeout + slack`, or drop the outer wrapper and rely solely on each POST's own `postCtx` bound. `--watch` has no outer per-push timeout, so this affects `--once` only. Governs R4.

Concurrency across sinks is deferred (see Deferred to Follow-Up Work), but see Open Questions: a chronically-dead sink under `--watch` pays its full 5-minute timeout serially on every push, degrading freshness to the healthy sinks, so a fast-fail connect timeout or per-sink backoff may be needed before this is truly deferrable. Governs R4, R7.

**KTD4. Per-sink source state, keyed by peer hostname** Replace the single `SinkURL` and shared counters in `SourceState` with per-sink records, each carrying last push time, last push count, totals, and last error. **Key by peer hostname, not URL:** the peer hostname is the stable pairing identity, whereas a sink URL is mutable (a tailnet IP or port edit would orphan a URL-keyed record and lose its history). Store the current URL as a field on the record so `status`/`doctor` can show it. Preserve the DBSC summary fields at the top level since the filtered cookie set is identical across sinks. `status` and `doctor` read the per-sink records. Governs R6.

---

## Implementation Units

### U1. Sinks list in SourceConfig with legacy synthesis

**Goal:** the config layer represents N sinks and still loads every legacy single-sink config unchanged.

**Requirements:** R1, R2.

**Dependencies:** none.

**Files:**
- `internal/config/config.go` — add `SinkTarget{URL, Peer}` and `Sinks []SinkTarget` on `SourceConfig`; keep legacy `Sink`/`Peer`.
- `internal/config/config_test.go` — load and marshal coverage.
- `examples/source.yaml` (and any multi-sink example added) — document the `sinks:` shape.

**Approach:**
1. Add a `SinkTarget` type with `URL` (`yaml:"url"`) and `Peer` (`yaml:"peer"`, the key hostname).
2. Add `Sinks []SinkTarget` (`yaml:"sinks,omitempty"`) to `SourceConfig`; leave `Sink` and `Peer` in place.
3. Add a resolver method (e.g. `ResolvedSinks()`) that returns `Sinks` when non-empty, else a one-element list synthesized from `Sink`+`Peer`. Every consumer calls this rather than reading fields directly, so there is one place the legacy fallback lives. When both `Sinks` and legacy `Sink.URL` are empty, return an empty list (no delivery) so a mis-written config fails visibly rather than POSTing to an empty URL.
4. **Writing is via string template, not `yaml.Marshal`.** No code marshals `SourceConfig` today; configs are written by `renderSourceYAML` (`internal/cli/wizard.go`). Extend that renderer to emit a `sinks:` block for multi-sink configs and keep the single-sink render for legacy. Do not introduce a `yaml.Marshal` path in this unit: `Sink SinkRef` is tagged `yaml:"sink"` without `omitempty` and yaml.v3 does not omit zero-value structs, so marshaling a multi-sink-only config would emit a stray empty `sink:` block. (If a future unit does add marshaling, change `Sink` to `*SinkRef` first.)

**Patterns to follow:** the existing `omitempty` refs in this file (`Cmux`, `CDP`, `LiveCDP`) and their doc-comment style; `renderSourceYAML` in `internal/cli/wizard.go` for the write path.

**Test scenarios:**
- A `source.yaml` with only legacy `sink:`/`peer:` resolves to a one-element sink list with the same URL and peer.
- A `source.yaml` with a two-element `sinks:` list resolves to both, and the legacy scalars are ignored when `sinks` is present.
- A config with neither `sinks` nor legacy `sink.url` resolves to an empty list (no delivery), not a one-element empty-URL sink.
- Decoding a hand-written multi-sink `sinks:` block yields the expected per-entry `url` and `peer` (decode, not marshal round-trip).

**Verification:** config tests pass; a hand-written legacy config and a multi-sink config both decode to the expected resolved list.

### U2. Fan-out push: seal and POST per sink, isolate failures

**Goal:** one push reads and filters once, then delivers to every resolved sink, continuing past a failed sink.

**Requirements:** R3, R4, R7.

**Dependencies:** U1.

**Files:**
- `internal/cli/source.go` — `runSource`, `pushWithFreshBlocklist`, `pushOnce`, `recordSourcePushResult`.
- `internal/cli/source_test.go` — fan-out and failure-isolation coverage.

**Approach:**
1. In `runSource`, resolve the sink list via `ResolvedSinks()`. Do **not** resolve secrets up front — `resolveTransportSecret` hard-errors on a missing key and would abort all sinks (per KTD2, this defeats R4).
2. In `pushOnce`, build the plaintext payload once (cookies after blocklist/DBSC filtering plus the secrets-bus payload — unchanged), then loop the sinks. Per sink: resolve `resolveTransportSecret(common.ConfigDir, sink.Peer, cfg.Security.SharedSecret)` (note the third arg is the legacy shared-secret string), `SealWithSecret(payload, secret)`, POST to `sink.URL` bounded by the per-sink timeout, capture a per-sink result.
3. Isolate errors: a secret-resolution, seal, or POST failure records that sink as failed and continues to the next sink. Never fall through to the shared secret on a missing per-sink key (KTD2). Aggregate into a whole/partial/total-failure outcome for the return value and exit status.
4. In `--once`, scale (or drop) the outer `context.WithTimeout` wrapper per KTD3 so a slow first sink cannot cancel later sinks.
5. Keep the existing stderr reply line per sink (`posted N cookies, sink replied: ...`) prefixed with the sink so three-sink output stays readable, and keep the single `secrets-bus: shipping N cli(s)` line (secrets are computed once).
6. `--watch` calls the same `pushOnce`, so fan-out on every cookie/secrets write falls out of the loop with no watcher change.

**Execution note:** start with a failing test that asserts a two-sink push where the first sink errors still delivers to the second and returns a partial-success status.

**Patterns to follow:** existing `pushOnce` sealing and POST with the `SyncClient` timeout profile; existing `recordSourcePushResult` shape.

**Test scenarios:**
- Two healthy sinks: one push seals twice with the two peer keys and POSTs to both URLs; both recorded ok.
- First sink POST times out, second healthy: second still receives the payload; outcome is partial success; first sink's error recorded.
- First sink's peer key is missing: that sink is recorded failed and the loop continues to the healthy second sink; the missing-key sink is NOT sealed under `Security.SharedSecret`.
- `--once` with a first sink that runs to its full per-sink timeout: later healthy sinks still get their own full timeout window and deliver (guards the scaled outer deadline from KTD3).
- All sinks fail: push returns total failure with each sink's error recorded.
- Single legacy sink (resolved one-element list): behavior and stderr identical to today.
- Cookies are read and filtered exactly once regardless of sink count (assert the read/filter path runs once for a three-sink config).

**Verification:** source tests pass; a manual `source --once` against two reachable local listeners delivers to both, and killing one listener leaves the other delivered with a partial-success exit.

### U3. Per-sink source state

**Goal:** push results are tracked per sink so status and doctor can report each target.

**Requirements:** R6.

**Dependencies:** U1.

**Files:**
- `internal/state/state.go` — `SourceState` per-sink records.
- `internal/state/state_test.go` — serialization and back-compat coverage.

**Approach:**
1. Add a per-sink record type keyed by peer hostname (per KTD4), carrying the current URL, `LastPush`, `LastPushCount`, `TotalPushes`, `TotalFailures`, `LastError`, `LastErrorAt`, and hold a list/map of them on `SourceState`.
2. Keep the top-level DBSC summary fields (`LastDBSCWarned/Skipped/Sample`) since the filtered set is shared across sinks.
3. Tolerate an old `sink_url`-shaped state file on first read after upgrade: `state.LoadSource` does a plain `json.Unmarshal` with no versioning, so keep the legacy `SinkURL` field decodable and migrate it into a one-element per-sink record rather than erroring.

**Patterns to follow:** existing `SourceState` JSON tags and the DBSC field doc comments.

**Test scenarios:**
- Recording two sinks' results yields two per-sink records with independent counters.
- A pre-upgrade state JSON with `sink_url` loads into a single per-sink record without error.
- A total-failure push increments the failing sink's `TotalFailures` and sets its `LastError`, leaving a healthy sink's counters untouched.

**Verification:** state tests pass; the state file after a two-sink push shows two records.

### U4. Additive wizard pairing for a new sink

**Goal:** grow an existing source config by pairing another peer and appending it to `sinks`, without overwriting existing sinks.

**Requirements:** R5.

**Dependencies:** U1.

**Files:**
- `internal/cli/wizard.go` — `runWizardInstall`, `wizardInstallSource`, `renderSourceYAML`, `beginSourcePairing` / the pairing-info writer, and the `guardConfigPeerMismatch` path.
- `internal/cli/wizard_test.go` — additive-path coverage.

**Approach:**
1. Add an additive mode (an `--add-sink` flag, or make `--peer` recognize that a source config already exists and append rather than overwrite). Explicitly not the current `--force`, which overwrites `source.yaml`.
2. In additive mode, skip the `guardConfigPeerMismatch` overwrite guard: load the existing config, pair the new peer, then append a `SinkTarget{URL, Peer}` to `Sinks`, migrating a legacy single-sink config to a two-element list on first add.
3. **File the new key under the operator-supplied `--peer` name via `beginSourcePairing` / the pairing-info writer — not `runPairAsSource`.** `runPairAsSource` stores the key under `res.RemotePeer` (the sink's announced `os.Hostname`, e.g. `e2b.local`), while `SinkTarget.Peer` and the fan-out lookup (`resolveTransportSecret(..., sink.Peer, ...)`) use the operator name (e.g. `instinct`). Filing under the announced name would leave the daemon looking up the wrong `keys/<peer>.json` at sync time and reporting connection-refused. Record the announced hostname separately (as the wizard already does), so `keys/<sink.Peer>.json` matches the name written into `Sinks`. This is the exact "announced as e2b.local, stored as instinct" behavior the wizard already handles for the single-sink case.
4. Render `sinks:` via `renderSourceYAML` when writing a multi-sink config; keep the legacy single-sink render for the first-time single install.
5. Leave the source LaunchAgent unchanged — it already runs `source --watch`, which now fans out via U2.

**Execution note:** this unit changes pairing/config-write flow; add a test that an add-sink against a legacy single-sink config yields a two-element `sinks` list with `keys/<sink.Peer>.json` present under the operator peer name, before wiring the flag.

**Patterns to follow:** existing `wizardInstallSource` write path, `beginSourcePairing` / pairing-info writer key-filing, `keystore.Path`, and `renderSourceYAML`.

**Test scenarios:**
- Add-sink against a legacy single-sink config produces a two-element `sinks` list and a second `keys/<sink.Peer>.json` named by the operator `--peer`, not the announced hostname.
- Add-sink against an existing multi-sink config appends a third entry and leaves the first two intact.
- Add-sink with a peer that is already present is a no-op (or clear error), not a duplicate entry.
- A first-time single install still writes the legacy single-sink shape and pairs one peer.

**Verification:** wizard tests pass; adding a sink to a live single-sink config leaves the original sink and key in place and the daemon then pushes to both.

### U5. Per-sink reporting in status and doctor

**Goal:** `status` and `doctor` show each sink's URL, last push, last error, and reachability.

**Requirements:** R6.

**Dependencies:** U3.

**Files:**
- `internal/cli/status.go` — render per-sink source state.
- `internal/cli/doctor.go` — per-sink source-state and reachability checks.
- `internal/cli/doctor_test.go` / `status_test.go` — per-sink output coverage.

**Approach:**
1. `status`: replace the single sink URL/last-push block with one block per configured sink from the per-sink state.
2. `doctor`: the existing "Source state" check reports per sink (last push age, last error); keep it green/warn per sink. Reachability, if probed, is reported per sink URL.
3. Keep output stable and readable for the common single-sink case (one block reads like today).

**Patterns to follow:** existing `doctor` check list and the `[OK]/[WARN]` line format; existing `status` config/state rendering.

**Test scenarios:**
- `status` with two sinks prints two sink blocks with independent last-push values.
- `doctor` with one stale sink and one fresh sink reports WARN for the stale one and OK for the fresh one.
- Single-sink config prints a single block matching today's shape.

**Verification:** status/doctor tests pass; `doctor` on a two-sink install reports both sinks.

### U6. Docs: README, CHANGELOG, example config

**Goal:** the multi-sink shape and the add-sink flow are documented, and the DBSC per-sink caveat is stated.

**Requirements:** R1, R5.

**Dependencies:** U1, U4.

**Files:**
- `README.md` — multi-sink `sinks:` config, the add-sink command, and the per-sink DBSC note.
- `CHANGELOG.md` — the multi-sink entry.
- `examples/` — a multi-sink `source.yaml` example.

**Approach:** document the `sinks:` list, the backward-compatible single-sink form, the add-sink flow from U4, and that device-bound cookies remain per-sink. Mirror the existing README section style.

**Test expectation:** none -- docs and example config, no behavioral change.

**Verification:** README renders; the example config decodes under U1's loader (a decode test over `examples/*.yaml` if one exists, otherwise manual).

---

## Scope Boundaries

**In scope:** the source side — config schema, fan-out push, per-sink state, additive wizard pairing, status/doctor reporting, docs.

**Out of scope:** the sink side (no sink behavior change — each sink still receives one sealed payload exactly as today), the transport/crypto (seal format and key derivation unchanged), DBSC handling (unchanged, and per-sink by nature), and the cmux local loop (`cmux-sync` is same-machine and independent of the push).

### Deferred to Follow-Up Work

- **Concurrent fan-out.** Sequential POSTs with per-sink timeouts are the initial approach (KTD3). Parallelizing the per-sink POSTs is a later optimization if the sink count grows past a handful.
- **A `remove-sink` / list-sinks management command.** This plan adds sinks; pruning one is a hand-edit for now. Treat this as a security control, not just convenience (see Open Questions): the interim manual revoke for a compromised or retired sink is to remove its `SinkTarget` entry **and** delete `keys/<peer>.json`, so it stops receiving the credential stream on the next watch cycle. Until `remove-sink` exists, that manual procedure is the revocation path.
- **Per-sink blocklist or domain filtering.** The blocklist stays global to the source; per-sink cookie narrowing is not in scope.

---

## System-Wide Impact

- **Existing single-sink users:** unaffected. Legacy configs load as a one-element list (KTD1) and the stderr and state shapes for one sink match today.
- **State file:** the on-disk `SourceState` shape changes; U3 migrates an old `sink_url` state on first read rather than erroring.
- **Operators:** `status`/`doctor` output grows a block per sink; single-sink output stays familiar.

---

## Open Questions

These are trust-model and delivery-behavior forks that need Matt's call; they do not block starting U1-U6 but should be settled before shipping.

- **OQ1 — Sink trust tiering.** Every sink receives the identical full cookie and secret payload, so the weakest sink's compromise exposes the whole credential set. Are all three sinks (instinct/e2b, Mac mini, grok-bot) accepted as equally trusted with the full payload, or does a lower-trust sink like grok-bot warrant a scoped payload (per-sink domain filtering, deferred here) before multi-sink ships? Default assumption if unanswered: all sinks equally trusted, stated as such.
- **OQ2 — Revocation as a first-class control.** Should `remove-sink` be pulled into this PR rather than deferred, given that without it a compromised or decommissioned sink keeps receiving fresh credentials until a manual key delete? Default: keep it deferred with the documented manual revoke procedure above.
- **OQ3 — Chronically-dead sink under `--watch`.** A sink that is off but reachable-then-timing-out costs its full 5-minute timeout serially on every cookie change, degrading freshness to healthy sinks. This is the exact three-machines-one-often-off case. Options: a short fast-fail connect timeout, per-sink backoff that skips a recently-failed sink, or bring concurrency (currently deferred) forward. Default: fast-fail connect timeout, backoff deferred.

---

## Verification Contract

- Package tests pass for `internal/config`, `internal/cli` (source, wizard, status, doctor), and `internal/state`.
- A legacy single-sink `source.yaml` produces byte-for-byte-equivalent push behavior and stderr for its one sink.
- A two-sink `source --once` delivers to both reachable listeners; with one listener down, the other is still delivered and the push reports partial success.
- In `--once`, a first sink that runs to its full per-sink timeout does not cancel later healthy sinks (the scaled outer deadline holds).
- A sink whose per-sink key is missing is isolated as a failure and is never sealed under the fleet-wide shared secret.
- `doctor` on a two-sink install reports both sinks with independent last-push/last-error state.
- Adding a sink via the wizard to a live single-sink config leaves the original sink and key intact, files the new key as `keys/<operator-peer>.json` (not the announced hostname), and the watcher then pushes to both.

## Definition of Done

- R1-R7 met.
- All units landed with their test scenarios covered.
- Legacy single-sink configs verified unchanged.
- README, CHANGELOG, and an example multi-sink config updated.
- No sink-side, transport, or DBSC behavior changed.
