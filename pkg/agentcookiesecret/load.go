package agentcookiesecret

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/mvanhorn/agentcookie/internal/envfile"
	"github.com/mvanhorn/agentcookie/internal/keystore"
)

// Source is where a particular key/value pair came from. Useful for
// debug / verbose loggers; consumers usually ignore it.
type Source int

const (
	SourceBusSealed Source = iota
	SourceBusPlain
	SourceFallback
	SourceEnv
)

// LoadResult is the structured form of Load's return value. Most callers
// just want the Env map, but library authors writing debug commands like
// "agentcookie secret list" find the per-key source useful.
type LoadResult struct {
	// Env is the merged map. Callers typically destructure this.
	Env map[string]string
	// Sources records where each key was resolved from. Same keys as Env.
	Sources map[string]Source
}

// ErrInvalidCLIName is returned when the cli-name argument does not
// satisfy the v1 spec naming rules. Surfaced explicitly so callers can
// distinguish "you passed a bad name" from "the bus has no entry for
// this cli."
var ErrInvalidCLIName = errors.New("agentcookiesecret: cli name violates v1 naming rules")

// Load resolves secrets for the given CLI from the agentcookie bus and
// returns them as a flat map. Equivalent to LoadWithFallback(cliName, "").
//
// Errors are returned for invalid cli-name, for a secrets.env that does
// not parse or cannot be read (the error names the file and line), and
// for unreadable sealed files when the master key is required. Missing
// bus files are NOT errors: Load falls through to process env and
// returns whatever env variables match. Callers can distinguish "no bus
// entry, env-only" by inspecting the result's Sources field.
func Load(cliName string) (map[string]string, error) {
	res, err := LoadDetailed(cliName, "")
	if err != nil {
		return nil, err
	}
	return res.Env, nil
}

// LoadWithFallback is like Load but consults a caller-supplied file path
// before falling through to process env. Useful for CLIs migrating to the
// bus while preserving compatibility with their existing config file
// during the transition.
//
// The fallback file must be in the same KEY=VALUE format the bus uses.
// Missing fallback files are silently ignored.
func LoadWithFallback(cliName string, fallbackPath string) (map[string]string, error) {
	res, err := LoadDetailed(cliName, fallbackPath)
	if err != nil {
		return nil, err
	}
	return res.Env, nil
}

// LoadDetailed returns the full LoadResult with per-key Sources, for
// callers that need to know provenance.
func LoadDetailed(cliName string, fallbackPath string) (*LoadResult, error) {
	if !validCLIName(cliName) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidCLIName, cliName)
	}

	res := &LoadResult{
		Env:     map[string]string{},
		Sources: map[string]Source{},
	}

	// Lowest priority first; later layers overwrite. Process env -> fallback
	// -> plaintext bus -> sealed bus. (Reading in that order, then the
	// final value for each key is the highest-priority source seen.)

	// 4. Process env (lowest priority).
	for _, kv := range os.Environ() {
		eq := strings.IndexByte(kv, '=')
		if eq < 1 {
			continue
		}
		k := kv[:eq]
		v := kv[eq+1:]
		// We don't have a list of "interesting" keys to pre-filter on.
		// Callers usually want everything; if they didn't, they would
		// not be using a env-shaped loader. Skip a few obviously-noisy
		// shell-internal keys to avoid polluting the map.
		switch k {
		case "PWD", "OLDPWD", "SHLVL", "_":
			continue
		}
		res.Env[k] = v
		res.Sources[k] = SourceEnv
	}

	// 3. Fallback file, if requested.
	if fallbackPath != "" {
		if fb, err := envfile.ReadFile(fallbackPath); err == nil {
			for k, v := range fb {
				res.Env[k] = v
				res.Sources[k] = SourceFallback
			}
		}
	}

	// 2. Plaintext bus file. A missing file just means no bus entry. Any
	// other failure (a syntax error, an unreadable file) is returned once
	// we know the plaintext file is the selected source: the spec forbids
	// skipping a bad file, since the caller would then run on env values
	// the bus was meant to override. When a sealed twin exists it is
	// authoritative (spec section 5.2) and a bad plaintext sibling is ignored.
	home, _ := os.UserHomeDir()
	plainPath := filepath.Join(home, ".agentcookie", "secrets", cliName, "secrets.env")
	sealedPath := plainPath + ".sealed"
	_, sealedStatErr := os.Stat(sealedPath)
	sealedPresent := sealedStatErr == nil

	plain, err := envfile.ReadFile(plainPath)
	if !sealedPresent {
		// A writer seals by creating the sealed file and then removing the
		// plaintext one. Check again so a seal that lands between the first
		// stat and the plaintext read is still found.
		_, sealedStatErr = os.Stat(sealedPath)
		sealedPresent = sealedStatErr == nil
	}
	switch {
	case err == nil:
		for k, v := range plain {
			res.Env[k] = v
			res.Sources[k] = SourceBusPlain
		}
	case !errors.Is(err, fs.ErrNotExist) && !sealedPresent:
		return res, fmt.Errorf("read %s: %w", plainPath, err)
	}

	// 1. Sealed bus file (highest priority).
	if sealedPresent {
		masterKey, mkErr := keystore.ReadMasterKey()
		if mkErr != nil {
			// Sealed file present but no master key. Caller almost
			// certainly wants to know about this; return the error.
			return res, fmt.Errorf("read master key for sealed file at %s: %w", sealedPath, mkErr)
		}
		raw, err := os.ReadFile(sealedPath)
		if err != nil {
			return res, fmt.Errorf("read sealed file %s: %w", sealedPath, err)
		}
		plain, err := keystore.Unseal(masterKey, raw)
		if err != nil {
			return res, fmt.Errorf("unseal %s: %w", sealedPath, err)
		}
		parsed, err := envfile.ParseBytes(plain)
		if err != nil {
			return res, fmt.Errorf("parse unsealed %s: %w", sealedPath, err)
		}
		for k, v := range parsed {
			res.Env[k] = v
			res.Sources[k] = SourceBusSealed
		}
	}

	return res, nil
}

func validCLIName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	if name[0] == '-' || name[len(name)-1] == '-' {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-':
		default:
			return false
		}
	}
	return true
}
