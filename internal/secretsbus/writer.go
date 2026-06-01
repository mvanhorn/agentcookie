package secretsbus

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mvanhorn/agentcookie/internal/keystore"
)

// WriteResult is the per-write outcome the sink uses for logging and
// sink-state reporting.
type WriteResult struct {
	// CLIsWritten is the number of per-CLI directories that received
	// at least one file write (env or sealed twin).
	CLIsWritten int
	// KeysWritten is the total count of KEY=VALUE pairs persisted
	// across all CLIs.
	KeysWritten int
	// SealedWritten is the number of `secrets.env.sealed` files written
	// (zero when sealing is disabled or the master key is absent).
	SealedWritten int
	// PlaintextWritten is the number of `secrets.env` files written.
	PlaintextWritten int
	// FilesMaterialized is the number of carried files written to 0600 files
	// under ~/.agentcookie/ (the base64 file-carriage path).
	FilesMaterialized int
}

// WritePayload persists the source-shipped secrets to disk under
// ~/.agentcookie/secrets/. Atomic write semantics: write to a sibling
// `.tmp` file, fsync, rename over the target. Every file is mode 0600.
//
// When sealing is requested AND the v0.12 master key is available in the
// Keychain, each per-CLI dataset is also written as `secrets.env.sealed`
// (the existing v0.12 sealed-envelope shape from internal/keystore).
//
// payload is the map carried in the wire envelope (envelope.Secrets).
// A nil or empty payload is a no-op and returns a zero WriteResult.
//
// Sealing policy decisions:
//   - `sealingEnabled = false` (the v0.12 default): writes only plaintext
//     `secrets.env`. No sealed twin.
//   - `sealingEnabled = true` AND master key present: writes sealed twin
//     alongside the plaintext (mirrors v0.12 cookies-sidecar behavior).
//   - `sealingEnabled = true` AND master key MISSING: writes plaintext only
//     and returns a non-fatal error so the sink can log without failing
//     the whole /sync.
func WritePayload(homeDir string, payload map[string]map[string]string, sealingEnabled bool) (WriteResult, []error) {
	var result WriteResult
	var errs []error
	if len(payload) == 0 {
		return result, nil
	}

	root := SecretsRoot(homeDir)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return result, []error{fmt.Errorf("mkdir secrets root %s: %w", root, err)}
	}

	var masterKey []byte
	if sealingEnabled {
		mk, err := keystore.ReadMasterKey()
		if err != nil {
			errs = append(errs, fmt.Errorf("sealing requested but master key unavailable: %w; writing plaintext only", err))
		} else {
			masterKey = mk
		}
	}

	for cliName, kv := range payload {
		if !validCLIName(cliName) {
			errs = append(errs, fmt.Errorf("refusing to write cli with invalid name %q (path traversal protection)", cliName))
			continue
		}
		// Filter out malformed keys defensively. The source should have
		// done this, but the sink does NOT trust the wire payload.
		safe := map[string]string{}
		for k, v := range kv {
			if !validKeyName(k) {
				errs = append(errs, fmt.Errorf("%s: dropping invalid key name %q", cliName, k))
				continue
			}
			safe[k] = v
		}
		// Carried-file materialization: decode any _FILE_<K>/K pairs to 0600
		// files under ~/.agentcookie/ and strip those keys so a carried file
		// never also leaks into the plaintext secrets.env. The sink does NOT
		// trust the wire payload; MaterializeFiles re-validates every target.
		matRes, consumed, matErrs := MaterializeFiles(homeDir, cliName, safe)
		errs = append(errs, matErrs...)
		result.FilesMaterialized += matRes.FilesWritten
		for k := range consumed {
			delete(safe, k)
		}

		if len(safe) == 0 {
			if matRes.FilesWritten > 0 {
				// Carried files only: count this CLI as written even though
				// no env keys remain for secrets.env.
				result.CLIsWritten++
			}
			continue
		}

		cliDir := filepath.Join(root, cliName)
		if err := os.MkdirAll(cliDir, 0o700); err != nil {
			errs = append(errs, fmt.Errorf("%s: mkdir: %w", cliName, err))
			continue
		}

		// Plaintext env file.
		envBytes := renderEnvFile(safe)
		envPath := filepath.Join(cliDir, "secrets.env")
		if err := atomicWrite(envPath, envBytes, 0o600); err != nil {
			errs = append(errs, fmt.Errorf("%s: write secrets.env: %w", cliName, err))
			continue
		}
		result.PlaintextWritten++

		// Sealed twin, when configured.
		if masterKey != nil {
			sealed, err := keystore.Seal(masterKey, envBytes)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: seal: %w", cliName, err))
			} else {
				sealedPath := filepath.Join(cliDir, "secrets.env.sealed")
				if werr := atomicWrite(sealedPath, sealed, 0o600); werr != nil {
					errs = append(errs, fmt.Errorf("%s: write secrets.env.sealed: %w", cliName, werr))
				} else {
					result.SealedWritten++
				}
			}
		}

		result.CLIsWritten++
		result.KeysWritten += len(safe)
	}
	return result, errs
}

// renderEnvFile produces the canonical secrets.env content for a map.
// Keys are sorted for deterministic output (helps tests and avoids
// noisy diffs on inspection). Values are written as-is, no quoting:
// the v1 spec accepts unquoted values for any string that doesn't need
// escape semantics, which is the common case for OAuth tokens, hex
// API keys, and base64-encoded payloads.
func renderEnvFile(kv map[string]string) []byte {
	keys := make([]string, 0, len(kv))
	for k := range kv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("# Written by agentcookie sink. See docs/spec-agentcookie-secrets-bus-v1.md for format.\n")
	b.WriteString("# Do not hand-edit while a sync is in progress: the next sync overwrites this file.\n")
	for _, k := range keys {
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(kv[k])
		b.WriteString("\n")
	}
	return []byte(b.String())
}

// atomicWrite writes data to path via a sibling .tmp + fsync + rename
// so a crashed write never leaves a partial file at the canonical path.
func atomicWrite(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
