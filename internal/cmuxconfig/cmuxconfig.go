// Package cmuxconfig performs targeted, comment-preserving edits to a
// user's cmux configuration (~/.config/cmux/cmux.json). cmux.json is JSONC
// (JSON with comments), which Go's encoding/json cannot round-trip without
// dropping comments and reordering keys, so edits are done as targeted
// regex substitutions that touch only the keys we own and leave the rest
// of the file -- comments, schema line, unrelated settings -- byte-stable.
//
// The only keys this package writes are automation.socketControlMode and
// automation.socketPassword, which the cmux local loop needs so a launchd
// agent (not a cmux child) can reach the control socket.
package cmuxconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ErrNotFound means ~/.config/cmux/cmux.json does not exist -- cmux has
// not written its config template yet (it does so on first launch).
// Callers treat this as "cmux not set up", not a hard failure.
var ErrNotFound = errors.New("cmuxconfig: cmux.json not found")

// DefaultConfigPath returns ~/.config/cmux/cmux.json.
func DefaultConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "cmux", "cmux.json"), nil
}

var (
	// Uncommented (line does not start with //) automation keys.
	scmRE  = regexp.MustCompile(`(?m)^(\s*"socketControlMode"\s*:\s*)"[^"]*"`)
	spRE   = regexp.MustCompile(`(?m)^(\s*"socketPassword"\s*:\s*)("[^"]*"|null)`)
	autoRE = regexp.MustCompile(`(?m)^(\s*"automation"\s*:\s*\{)`)
)

// SetSocketControlMode sets automation.socketControlMode (and, when
// password is non-empty, automation.socketPassword) in the cmux config at
// path. It backs the file up to "<path>.<timestamp>.bak" first and
// preserves comments and all other settings. now is injected so callers
// (and tests) control the backup timestamp. Returns the backup path.
//
// Idempotent: re-running with the same mode rewrites the same value.
func SetSocketControlMode(path, mode, password string, now time.Time) (backupPath string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrNotFound
		}
		return "", err
	}

	backupPath = fmt.Sprintf("%s.%s.bak", path, now.Format("20060102-150405"))
	if err := os.WriteFile(backupPath, data, 0o600); err != nil {
		return "", fmt.Errorf("cmuxconfig: backup: %w", err)
	}

	out := applyMode(string(data), mode, password)
	if err := os.WriteFile(path, []byte(out), 0o600); err != nil {
		return "", fmt.Errorf("cmuxconfig: write: %w", err)
	}
	return backupPath, nil
}

// applyMode returns src with socketControlMode (and optionally
// socketPassword) set, choosing among three shapes: an existing
// uncommented key, an existing uncommented automation block, or no
// automation block at all.
func applyMode(src, mode, password string) string {
	// strconv.Quote-d values can contain `$`, which regexp ReplaceAllString
	// would interpret as a capture-group reference and corrupt the value
	// (Greptile finding). escapeRepl doubles `$` so the value is inserted
	// literally; group refs like ${1} are added separately and stay live.
	modeQ := escapeRepl(strconv.Quote(mode))

	// Case 1: an uncommented socketControlMode already exists -> replace it.
	if scmRE.MatchString(src) {
		src = scmRE.ReplaceAllString(src, "${1}"+modeQ)
		if password != "" {
			src = setPasswordInExisting(src, password)
		}
		return src
	}

	// keys is inserted via string concatenation (case 3) or ReplaceAllString
	// (case 2); escape it for the ReplaceAllString path. Concatenation in
	// case 3 is unaffected by escaping a `$$` because... it is NOT -- so build
	// two forms: a literal (for case 3) and an escaped (for case 2).
	keysLiteral := `    "socketControlMode": ` + strconv.Quote(mode)
	if password != "" {
		keysLiteral += ",\n    \"socketPassword\": " + strconv.Quote(password)
	}

	// Case 2: an uncommented automation block exists -> inject keys after `{`.
	if autoRE.MatchString(src) {
		return autoRE.ReplaceAllString(src, "${1}\n"+escapeRepl(keysLiteral)+",")
	}

	// Case 3: no automation block -> insert one after the root opening brace.
	// Plain string concatenation here, so no regexp escaping needed.
	block := "\n  // Added by agentcookie: lets the launchd cmux-sync agent reach\n" +
		"  // cmux's control socket. Restart cmux to apply.\n" +
		"  \"automation\": {\n" + keysLiteral + "\n  },\n"
	if i := indexOfFirstBrace(src); i >= 0 {
		return src[:i+1] + block + src[i+1:]
	}
	return src
}

// setPasswordInExisting sets socketPassword when an automation block with a
// socketControlMode already exists. Replaces an existing socketPassword, or
// injects one on the line after socketControlMode (matching the cmux.json
// 4-space inner indent). The password value is escaped for ReplaceAllString.
func setPasswordInExisting(src, password string) string {
	pwQ := escapeRepl(strconv.Quote(password))
	if spRE.MatchString(src) {
		return spRE.ReplaceAllString(src, "${1}"+pwQ)
	}
	return scmRE.ReplaceAllString(src, "${0},\n    \"socketPassword\": "+pwQ)
}

// escapeRepl doubles `$` so a value used as a regexp.ReplaceAllString
// replacement is inserted literally rather than read as a $-group ref.
func escapeRepl(s string) string {
	return strings.ReplaceAll(s, "$", "$$")
}

func indexOfFirstBrace(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == '{' {
			return i
		}
	}
	return -1
}
