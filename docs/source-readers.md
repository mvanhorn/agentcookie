# Source reader boundary

`internal/source.Reader` owns browser reads, capabilities, watch paths and diagnostics. The CLI applies explicit selectors through `config.EffectiveSource` before a default path or credential is selected. A previously computed path is recomputed when selectors change; an explicit Chromium `chrome.db_path` remains explicit and also selects its storage directories. The target side of `profile-sync` and the sidecar consumer remain destination APIs, not source adapters.

The Chromium file reader preserves the existing immutable SQLite and optional Local Storage / IndexedDB tarball lanes. Partial packing failures are represented as incomplete and reported without losing successful cookie reads. The CDP reader supports cookies only and never opens profile databases or invokes a credential provider. Missing providers fail closed rather than defaulting to the host Keychain. Gecko readers can implement this interface without importing CLI internals.

## Version boundary

`protocol.Version` stays 2 in this change. `Version3`, optional envelope metadata, snapshot surface/site status, and storage IR v1 are contracts only. A sender must negotiate capabilities before using them. `complete` permits owned-absent deletion, `incomplete` preserves previous state, `unsupported` never touches a surface, `held` preserves a site, and `excluded` revokes owned state. Storage values and all DOMString IndexedDB identifiers use tagged JSON to avoid lossy UTF-16 conversion. Full graph validation and codec execution belong to the storage implementation, not these type declarations.

## Durable counters

`source-generation.json` contains the semantic generation. `source-state.json` retains the transport `last_sequence` alongside status. Read-modify-write holds a separate OS file lock across processes, creates 0600 temporary files, fsyncs the file, renames it, and fsyncs the directory. Status writers preserve counter high-water marks. Allocation returns only after persistence; corrupt or exhausted state is rejected. `ReseedGeneration` and `ReseedSequence` accept a minimum from authenticated reconciliation. They never lower state and do not reset sink replay history.

## Hermetic tests

`internal/e2e.New` owns a temporary HOME, XDG_CONFIG_HOME and TMPDIR. Synthetic source profiles use a fixed fixture password. The in-process harness executes the real source and sink pipeline with `--skip-secrets` and JSON behavior, an injected password, no master key, and an empty adapter registry. Forbidden credential providers panic before host I/O. Browser launches always use a temporary user-data-dir, mock keychain, basic password store and a loopback debug port. Cleanup owns browser exit, server shutdown and directory removal, including failure paths.

Run `AGENTCOOKIE_E2E_REQUIRED=1 go test ./internal/e2e`. Set `AGENTCOOKIE_CHROME_BINARY` for a non-default Chromium executable. A missing binary fails in required mode; a browser startup failure always fails rather than becoming a skip. Linux requires `strace` for the subprocess CDP-reader file-open guard. The trace follows only the reader, not the fixture browser, and rejects any running-profile file open.

The wire-to-browser fixture asserts host-only versus parent-domain delivery, `__Host-`, insecure HTTP delivery, unspecified SameSite, finite versus session expiry, and CHIPS in matching and different top-level contexts for both ancestor-bit values. Cold and live cookie shapers are also compared directly. The historical native Keychain timing test requires the separate `keychain_integration` build tag and an isolated test Keychain; it is never part of this harness or default CI.
