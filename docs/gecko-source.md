# Zen / Firefox source setup (experimental)

The Gecko source and live Chrome destination are **experimental**. Passing unit
or mock-browser tests is not evidence that the full browser lifecycle works.
Promotion requires the Linux Firefox/Chrome matrix and pinned headed macOS Zen
matrix, including restart, logout, storage, hostile service-worker silence,
compatibility and replay gates. See [release gates](release-gates.md).

## Choose the source, not a guessed profile

Ask **“Which browser do you log in with?”** before installation. Choose `zen`,
`firefox`, `librewolf`, or `floorp`; Chromium sources remain supported.
Use `--profile` to select a profile explicitly if the install-aware resolver
cannot choose one. It reports the canonical directory, creation identity, `How`,
and computed install hash. A stale or unmatched installation section is not
permission to silently use another profile. Mozilla uses **CityHash64 original
v1**, not modern CityHash, for the install hash.

```sh
agentcookie wizard install --as source --browser zen --peer mini
```

Review the dry-read summary before pairing or daemon installation: reader mode
and reason, profile identity, catalog counts, cookie/storage counts, held sites,
and effective permissions. Resolve ambiguous sites with `--containers` and
`--container-sites`, or explicitly accept holding them. A held site is not an
empty authoritative snapshot and must not cause deletion at the destination.

The wizard writes BiDi 9225 and companion 9226 to avoid the owned Chrome port
9224. Existing readers may default to 9224/9225; use the printed explicit ports.

Use `gecko-launcher install --browser zen --profile <selector> --bidi-port 9225` to create an
opt-in launcher. Quit the existing browser normally, then start via the launcher.
The launcher enables a loopback BiDi port; the broker owns the session so daemon
restarts can reconnect without closing the browser. Never kill a foreign BiDi
session or read a running profile's database to work around missing permissions.

The temporary companion has `cookies`, `contextualIdentities`, `tabs`, `tabHide`,
`webNavigation`, `scripting`, and `<all_urls>` permissions. It can read cookies and
site storage across the chosen profile. Firefox/Zen may display a one-time
“hiding tabs” notice. Installing a launcher does not silently grant permission
to install the companion or start a browser.

`storage_read_mode: intercept` is the default across config loading and
onboarding. It remains experimental: the combined-path and hostile-service-worker
headed gates have not passed, so no zero-network claim is made. An unrun gate
does not activate a request-mode fallback. Explicit `storage_read_mode: request`
makes a cookie-bearing GET to each origin per read and may trigger server-side
activity; onboarding discloses this before companion/storage-preview consent.

When the browser is closed, file mode reads a private copy and supplies
cookies only, never an authoritative logout snapshot. A profile lock, unknown
lock status, or running browser blocks file fallback. Broker loss pauses BiDi
storage work; cookie and explicitly supported request-mode paths can continue.

## Choose the destination

New GUI Mac installs default to `live_chrome`, an owned Chrome profile, not your
normal Chrome profile. Linux and non-GUI installs default to `attach`, using an
already-running loopback CDP browser. Existing installations keep their current
configuration. `ppcli` remains an explicit legacy destination.

```sh
agentcookie wizard install --as sink --peer laptop --code <code> --pair-url <url> \
  --destination live_chrome
```

Review [source-zen.yaml](../examples/source-zen.yaml) and
[sink-live-chrome.yaml](../examples/sink-live-chrome.yaml). The owned Chrome starts
without a window, restores accepted desired state, verifies values, and becomes
`ready` before opening its first window. `agentcookie Chrome.app` asks the running
sink to open the destination; it does not bypass readiness by starting Chrome.
Run `agentcookie doctor` and `agentcookie status --json`: an HTTP acknowledgement
or an accepted generation alone is not success. Look for Destination `ready`,
applied generations, pending work, failed databases, and `repair-needed`.

Clean browser close respects the configured relaunch policy. Desired state can
be accepted while Chrome is closed; reopening must restore it before a window.
A launch failure or pending/repair state blocks successful onboarding.

## Limits that affect accounts

- Sessions bound to a device, engine, or server fingerprint can require a new
  login after switching from Gecko to Chrome; sync cannot remove that binding.
- Gecko's DBSC classification is **not applicable**, not “all sessions portable.”
- Browser containers are account boundaries. Ambiguous same-site accounts stay
  held until selected; multiple simultaneous accounts need separate destinations.
- Unsupported partition attributes and unknown origin attributes are counted and
  excluded rather than flattened into an unpartitioned account.
- Chromium cold-file storage remains a separate, narrowed legacy lane. Gecko
  storage uses per-origin records, not cross-engine LevelDB directory copying.
- IndexedDB schema repair may require destination version `V+1`. Sites opening a
  fixed lower version then receive `VersionError`; do not hide this as success.
- Database, store and index names use tagged UTF-16 so lone surrogates survive.
- Losing source generation state requires an authenticated reseed. Never reset
  replay counters or clear sink state to make an old envelope acceptable.

If the temporary companion is removed, restart the browser through the configured
launcher when instructed; the source does not repeatedly take over BiDi sessions.
Broker loss keeps available companion cookies flowing while storage diagnostics
report the pause. A source started before the browser can upgrade to live mode
when the matching browser appears. These lifecycle behaviors remain gated by
the actual browser tests, not just unit mocks.

### Destination diagnostics and optional authentication probes

`doctor` reports each destination's running/readiness state, accepted and applied
counters, pending/failed counts, conflicts, cap exclusions and repair state. A
required destination pending for over ten minutes is a failure only while new
envelopes have arrived within the last ten minutes. The pending interval survives
new generations and restarts; an old state file without timing data is not enough
to claim a timed failure. A stopped source is diagnosed separately.

`probe_hosts: [github.com]` on an owned `live_chrome` destination explicitly opts
in to a real site navigation during `doctor`. It reuses the existing fixed
`VerifySpec` predicate in an isolated CDP world and returns only an authentication
boolean. No cookie, token, DOM value or raw browser error is printed. No probe
runs by default, for unsupported hosts, for attach destinations, or before the
owned destination is running and ready. The private profile/PID/browser receipt
is checked before opening a fresh hidden target; a changed browser incarnation
fails closed. The target is closed afterward, and doctor never launches the
browser or opens its first visible window. Probe requests can reach the selected
site, including its ordinary page resources and redirects. Delivery readiness
and a usable authenticated session are separate results.
