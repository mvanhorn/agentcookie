package agentcookiesecret

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
// Errors are returned for invalid cli-name and for unreadable sealed
// files when the master key is required. Missing bus files are NOT
// errors: Load falls through to process env and returns whatever env
// variables match. Callers can distinguish "no bus entry, env-only" by
// inspecting the result's Sources field.
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
		if fb, err := readEnvFile(fallbackPath); err == nil {
			for k, v := range fb {
				res.Env[k] = v
				res.Sources[k] = SourceFallback
			}
		}
	}

	// 2. Plaintext bus file.
	home, _ := os.UserHomeDir()
	plainPath := filepath.Join(home, ".agentcookie", "secrets", cliName, "secrets.env")
	if plain, err := readEnvFile(plainPath); err == nil {
		for k, v := range plain {
			res.Env[k] = v
			res.Sources[k] = SourceBusPlain
		}
	}

	// 1. Sealed bus file (highest priority).
	sealedPath := plainPath + ".sealed"
	if _, err := os.Stat(sealedPath); err == nil {
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
		parsed, err := parseEnvBytes(plain)
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

// readEnvFile is a minimal v1-conformant .env parser. Mirrors the strict
// subset documented in the spec so this package has zero non-stdlib
// dependencies beyond keystore (which is needed only for sealed-file
// unseal).
func readEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 256*1024)
	return parseEnvScanner(scanner)
}

// parseEnvBytes parses a byte slice via the same scanner-based grammar
// as readEnvFile. Used after sealed-file unseal returns plaintext bytes.
func parseEnvBytes(data []byte) (map[string]string, error) {
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	scanner.Buffer(make([]byte, 0, 64*1024), 256*1024)
	return parseEnvScanner(scanner)
}

func parseEnvScanner(scanner *bufio.Scanner) (map[string]string, error) {
	out := map[string]string{}
	lineNum := 0
	var pending strings.Builder
	var pendingKey string
	inContinuation := false

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		if inContinuation {
			if strings.HasSuffix(line, "\\") {
				pending.WriteString(line[:len(line)-1])
				continue
			}
			pending.WriteString(line)
			out[pendingKey] = stripQuotes(pending.String())
			pending.Reset()
			pendingKey = ""
			inContinuation = false
			continue
		}

		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			return nil, fmt.Errorf("line %d: missing '=' (expected KEY=VALUE)", lineNum)
		}
		key := line[:eq]
		if key != strings.TrimRight(key, " \t") || key != strings.TrimLeft(key, " \t") {
			return nil, fmt.Errorf("line %d: whitespace around '=' is not allowed", lineNum)
		}
		if !validKeyName(key) {
			return nil, fmt.Errorf("line %d: invalid key name %q", lineNum, key)
		}
		value := line[eq+1:]

		if strings.HasSuffix(value, "\\") {
			pending.WriteString(value[:len(value)-1])
			pendingKey = key
			inContinuation = true
			continue
		}
		out[key] = stripQuotes(value)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan: %w", err)
	}
	if inContinuation {
		return nil, fmt.Errorf("unterminated backslash continuation for key %q", pendingKey)
	}
	return out, nil
}

func stripQuotes(v string) string {
	if len(v) >= 2 {
		first, last := v[0], v[len(v)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			return v[1 : len(v)-1]
		}
	}
	return v
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

func validKeyName(k string) bool {
	if k == "" {
		return false
	}
	for i, r := range k {
		isLetter := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		isDigit := r >= '0' && r <= '9'
		isUnder := r == '_'
		if i == 0 {
			if !(isLetter || isUnder) {
				return false
			}
			continue
		}
		if !(isLetter || isDigit || isUnder) {
			return false
		}
	}
	return true
}
