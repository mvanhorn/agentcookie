package secretsbus

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// File carriage lets the secrets bus carry an arbitrary FILE (a multiline PEM,
// a TOML config) from source to sink and materialize it on the sink as a
// mode-0600 file under ~/.agentcookie/.
//
// The wire envelope is a flat map[string]map[string]string (per-CLI -> key ->
// value), so a file cannot ride as a raw multiline value. Each carried file
// rides as TWO single-line keys in the CLI's env map:
//
//   - the declared key K, whose value is the base64 encoding of the file bytes
//     (single-line, dotenv-safe);
//   - a reserved companion key _FILE_<K>, whose value is the relative target
//     path (under ~/.agentcookie/) the sink materializes K to.
//
// The companion key is the carriage instruction: it travels in the same flat
// envelope so the sink needs no copy of the manifest (manifests stay
// source-side per the v2 spec). Reserved keys are underscore-prefixed and pass
// through the v1 dotenv grammar unchanged.

// fileTargetKeyPrefix is the reserved prefix for the companion key that tells
// the sink where to materialize a carried file. _FILE_<K> holds the relative
// target path for the carried payload under key K.
const fileTargetKeyPrefix = "_FILE_"

// maxCarriedFileBytes caps a single carried file at 256 KB (decoded), matching
// the v1 secrets.env size cap. Oversized payloads are refused, not written, so
// a runaway file cannot swamp the sink.
const maxCarriedFileBytes = 256 * 1024

// CarryFileKey returns the reserved companion key for a carried-file payload
// key. The sink scans for keys with this prefix to find materialization
// instructions.
func CarryFileKey(payloadKey string) string {
	return fileTargetKeyPrefix + payloadKey
}

// CarryFiles reads each enabled [[files]] item from a manifest, base64-encodes
// its source file, and returns the per-key additions to inject into the CLI's
// env map: the base64 payload under item.Key plus the companion target under
// _FILE_<item.Key>.
//
// enabled is the set of opt-in (Optional=true) item keys the user has turned
// on. A default item (Optional=false) is always carried; an optional item is
// carried only when enabled[item.Key] is true. This implements the opt-in /
// discovery-does-not-carry-unless-enabled rule.
//
// homeDir expands a leading ~/ in each item's Source. Missing source files,
// oversized files, and invalid targets accumulate as non-fatal errors so one
// bad item does not abort the push; the returned map omits failed items.
func CarryFiles(files []ManifestV2File, enabled map[string]bool, homeDir string) (map[string]string, []error) {
	out := map[string]string{}
	var errs []error
	for i := range files {
		f := &files[i]
		if f.Optional && !enabled[f.Key] {
			continue // opt-in item the user has not enabled: do not carry.
		}
		// Re-validate the target defensively even though parse validated it;
		// a manifest could be hand-built and reach here without ParseManifestV2.
		if err := validateMaterializeTarget(f.Target); err != nil {
			errs = append(errs, fmt.Errorf("file item %q: %w", f.Key, err))
			continue
		}
		if !validKeyName(f.Key) {
			errs = append(errs, fmt.Errorf("file item %q: invalid carry key", f.Key))
			continue
		}
		src := expandHome(f.Source, homeDir)
		info, err := os.Stat(src)
		if err != nil {
			if os.IsNotExist(err) {
				errs = append(errs, fmt.Errorf("file item %q: source missing: %s", f.Key, src))
			} else {
				errs = append(errs, fmt.Errorf("file item %q: stat %s: %w", f.Key, src, err))
			}
			continue
		}
		if info.IsDir() {
			errs = append(errs, fmt.Errorf("file item %q: source %s is a directory", f.Key, src))
			continue
		}
		if info.Size() > maxCarriedFileBytes {
			errs = append(errs, fmt.Errorf("file item %q: source %s is %d bytes, over the %d byte limit; not carrying", f.Key, src, info.Size(), maxCarriedFileBytes))
			continue
		}
		data, err := os.ReadFile(src)
		if err != nil {
			errs = append(errs, fmt.Errorf("file item %q: read %s: %w", f.Key, src, err))
			continue
		}
		out[f.Key] = base64.StdEncoding.EncodeToString(data)
		out[CarryFileKey(f.Key)] = f.Target
	}
	return out, errs
}

// LoadEnabledFileKeys reads the per-CLI opt-in set for carried files. Opt-in
// keys live one-per-line in ~/.agentcookie/file-optin/<cli>.keys; blank lines
// and # comments are ignored. A missing file means "nothing opted in" (the
// default), so an optional [[files]] item is not carried until the user adds
// its key here. This is the discovery-does-not-carry-unless-enabled gate for
// opt-in items; default (non-optional) items ignore this set entirely.
func LoadEnabledFileKeys(homeDir, cliName string) map[string]bool {
	enabled := map[string]bool{}
	p := filepath.Join(agentcookieRoot(homeDir), "file-optin", cliName+".keys")
	f, err := os.Open(p)
	if err != nil {
		return enabled // missing or unreadable: nothing opted in.
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		enabled[line] = true
	}
	return enabled
}

// MaterializeResult reports the outcome of MaterializeFiles for sink logging.
type MaterializeResult struct {
	// FilesWritten is the count of carried files materialized to 0600 files.
	FilesWritten int
}

// MaterializeFiles scans a per-CLI env map for carried-file companion keys
// (_FILE_<K>), decodes the base64 payload under K, and writes the bytes to a
// 0600 file under ~/.agentcookie/ at the companion's relative target. It
// returns the decoded payload key set so the caller can strip those keys (and
// their companions) from the env map before it is written as plaintext
// secrets.env (a carried file should not also leak as an env var).
//
// Security invariants (refuse rather than write insecurely):
//   - The target is resolved under ~/.agentcookie/ ONLY. A target that is
//     absolute, contains "..", or otherwise resolves outside ~/.agentcookie/
//     is refused with an error and nothing is written.
//   - The file is written mode 0600 (never group/world readable) via the
//     atomic write path (sibling .tmp + fsync + rename), so an existing file
//     is overwritten value-preserved/atomically.
//   - An oversized decoded payload is refused.
//
// The returned consumedKeys set contains both the payload key K and its
// companion _FILE_<K> for every successfully materialized file.
func MaterializeFiles(homeDir string, cliName string, env map[string]string) (MaterializeResult, map[string]bool, []error) {
	var result MaterializeResult
	consumed := map[string]bool{}
	var errs []error

	root := agentcookieRoot(homeDir)

	// Deterministic-ish: collect companion keys first.
	for k, target := range env {
		if !strings.HasPrefix(k, fileTargetKeyPrefix) {
			continue
		}
		payloadKey := strings.TrimPrefix(k, fileTargetKeyPrefix)
		if payloadKey == "" {
			errs = append(errs, fmt.Errorf("%s: companion key %q has no payload key", cliName, k))
			continue
		}
		b64, ok := env[payloadKey]
		if !ok {
			errs = append(errs, fmt.Errorf("%s: companion key %q present but payload key %q missing", cliName, k, payloadKey))
			// Still consume the dangling companion so it does not leak.
			consumed[k] = true
			continue
		}

		// The sink does NOT trust the wire: re-validate the target.
		if err := validateMaterializeTarget(target); err != nil {
			errs = append(errs, fmt.Errorf("%s: refusing to materialize %q: %w", cliName, payloadKey, err))
			continue
		}

		decoded, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: payload %q is not valid base64: %w", cliName, payloadKey, err))
			continue
		}
		if len(decoded) > maxCarriedFileBytes {
			errs = append(errs, fmt.Errorf("%s: payload %q decodes to %d bytes, over the %d byte limit; not writing", cliName, payloadKey, len(decoded), maxCarriedFileBytes))
			continue
		}

		dest, err := safeJoinUnderRoot(root, target)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: refusing to materialize %q: %w", cliName, payloadKey, err))
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			errs = append(errs, fmt.Errorf("%s: mkdir for %q: %w", cliName, payloadKey, err))
			continue
		}
		if err := atomicWrite(dest, decoded, 0o600); err != nil {
			errs = append(errs, fmt.Errorf("%s: write %q to %s: %w", cliName, payloadKey, dest, err))
			continue
		}
		result.FilesWritten++
		consumed[payloadKey] = true
		consumed[k] = true
	}

	return result, consumed, errs
}

// agentcookieRoot returns the absolute ~/.agentcookie directory.
func agentcookieRoot(homeDir string) string {
	return filepath.Join(homeDir, ".agentcookie")
}

// safeJoinUnderRoot joins a relative target under root and verifies the result
// stays inside root after symlink-agnostic path cleaning. Returns an error
// rather than a path that escapes root.
func safeJoinUnderRoot(root, rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("target %q must be relative", rel)
	}
	joined := filepath.Join(root, rel)
	cleanRoot := filepath.Clean(root)
	// Ensure joined is cleanRoot itself (rejected: must be a file under it) or
	// a descendant separated by a path boundary.
	if joined == cleanRoot {
		return "", fmt.Errorf("target %q resolves to the agentcookie root itself", rel)
	}
	if !strings.HasPrefix(joined, cleanRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("target %q escapes ~/.agentcookie/", rel)
	}
	return joined, nil
}

// expandHome expands a leading ~/ (or bare ~) in p against homeDir. Other
// paths are returned unchanged.
func expandHome(p, homeDir string) string {
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(homeDir, p[2:])
	}
	if p == "~" {
		return homeDir
	}
	return p
}
