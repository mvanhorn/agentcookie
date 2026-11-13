// Package envfile owns the v1 secrets.env grammar (spec section 3): one
// parser that every Go reader uses, and the renderer every writer uses, so a
// value written by agentcookie reads back unchanged everywhere.
//
// The parser matches the Python reader in clients/python. Its error strings
// never carry text from the file, since a malformed line can be a secret.
package envfile

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
)

// MaxLineBytes is the longest raw line a reader accepts, minus one: a line of
// this many bytes or more is an error in both the Go and the Python reader.
const MaxLineBytes = 256 * 1024

// Entry is one KEY=VALUE pair in file order.
type Entry struct {
	Key   string
	Value string
	// Line is the line the entry starts on (1-based).
	Line int
	// Continued reports that the value used a backslash continuation.
	Continued bool
}

// Options select the parse mode.
type Options struct {
	// LenientKeys accepts any key that has no whitespace around '=', for
	// importing a .env file that was not written for the bus. The caller
	// decides what to do with keys that fail ValidKey.
	LenientKeys bool
}

// Parse reads the strict v1 grammar from r and returns its entries in file
// order. A later entry for the same key wins when the entries become a map.
func Parse(r io.Reader, opts Options) ([]Entry, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), MaxLineBytes)

	var entries []Entry
	lineNum := 0
	var pending strings.Builder
	var pendingKey string
	var pendingLine int
	inContinuation := false

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		if lineNum == 1 {
			// Spec section 3.1: a leading UTF-8 BOM is permitted and ignored.
			line = strings.TrimPrefix(line, "\ufeff")
		}

		if inContinuation {
			if strings.HasSuffix(line, "\\") {
				pending.WriteString(line[:len(line)-1])
				continue
			}
			pending.WriteString(line)
			entries = append(entries, Entry{Key: pendingKey, Value: stripQuotes(pending.String()), Line: pendingLine, Continued: true})
			pending.Reset()
			pendingKey = ""
			inContinuation = false
			continue
		}

		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("line %d: missing '=' (expected KEY=VALUE)", lineNum)
		}
		if key != strings.Trim(key, " \t") {
			return nil, fmt.Errorf("line %d: whitespace around '=' is not allowed", lineNum)
		}
		if !opts.LenientKeys && !ValidKey(key) {
			// Never echo the key: a bare secret pasted on its own line
			// lands here.
			return nil, fmt.Errorf("line %d: invalid key name", lineNum)
		}

		if strings.HasSuffix(value, "\\") {
			pending.WriteString(value[:len(value)-1])
			pendingKey = key
			pendingLine = lineNum
			inContinuation = true
			continue
		}
		entries = append(entries, Entry{Key: key, Value: stripQuotes(value), Line: lineNum})
	}
	if err := scanner.Err(); err != nil {
		// The scanner stops on the line after the last one it returned.
		return nil, fmt.Errorf("line %d: scan: %w", lineNum+1, err)
	}
	if inContinuation {
		return nil, fmt.Errorf("line %d: unterminated backslash continuation (started on line %d)", lineNum, pendingLine)
	}
	return entries, nil
}

// ParseBytes parses data with the strict grammar and returns the key map.
func ParseBytes(data []byte) (map[string]string, error) {
	entries, err := Parse(bytes.NewReader(data), Options{})
	if err != nil {
		return nil, err
	}
	return ToMap(entries), nil
}

// ReadFile opens path and parses it with the strict grammar. A missing file
// returns an error that matches fs.ErrNotExist.
func ReadFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	entries, err := Parse(f, Options{})
	if err != nil {
		return nil, err
	}
	return ToMap(entries), nil
}

// ToMap folds entries into a map; a later entry for a key wins.
func ToMap(entries []Entry) map[string]string {
	out := make(map[string]string, len(entries))
	for _, e := range entries {
		out[e.Key] = e.Value
	}
	return out
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

// ValidKey reports whether k is a v1 key: an ASCII letter or underscore, then
// letters, digits and underscores.
func ValidKey(k string) bool {
	if k == "" {
		return false
	}
	for i, r := range k {
		isLetter := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		isDigit := r >= '0' && r <= '9'
		isUnder := r == '_'
		if i == 0 {
			if !isLetter && !isUnder {
				return false
			}
			continue
		}
		if !isLetter && !isDigit && !isUnder {
			return false
		}
	}
	return true
}

// Refusal reasons. A refusal never carries the value.
var (
	ErrNewline       = errors.New("value contains a newline")
	ErrTrailingCR    = errors.New("value ends in a carriage return")
	ErrUnquotable    = errors.New("value needs quoting but contains both quote characters it could be quoted with")
	ErrLineTooLong   = errors.New("value is too long for one secrets.env line (limit 256 KiB)")
	ErrShellUnsafe   = errors.New("value cannot be printed so that both a shell and the readers get it unchanged")
	ErrInvalidKey    = errors.New("invalid key name")
	ErrShellReserved = errors.New("key names a shell variable that the shell evaluates on assignment")
)

// RenderValue returns v in the form a secrets.env line carries, so the
// readers return exactly v. A value that reads back unchanged bare is left
// bare, so files that were fine before are rewritten byte for byte. Otherwise
// it is single-quoted when it has no single quote, double-quoted when it has
// no double quote and no backslash, and refused when neither works. Values
// with a newline or a trailing carriage return are refused, since the line
// format cannot carry them.
func RenderValue(v string) (string, error) {
	if strings.Contains(v, "\n") {
		return "", ErrNewline
	}
	if strings.HasSuffix(v, "\r") {
		return "", ErrTrailingCR
	}
	if bareSafe(v) {
		return v, nil
	}
	return quote(v, `"\`, ErrUnquotable)
}

// RenderLine returns the full KEY=VALUE line (without the newline) for key
// and value, refusing a value RenderValue refuses or a line the readers
// would reject as too long.
func RenderLine(key, v string) (string, error) {
	if !ValidKey(key) {
		return "", ErrInvalidKey
	}
	rv, err := RenderValue(v)
	if err != nil {
		return "", err
	}
	line := key + "=" + rv
	if len(line) >= MaxLineBytes {
		return "", ErrLineTooLong
	}
	return line, nil
}

// RenderSorted renders each pair with RenderLine, sorted by key. Keys whose
// pair RenderLine refuses are left out of lines and returned in refused.
func RenderSorted(kv map[string]string) (lines, refused []string) {
	for _, k := range slices.Sorted(maps.Keys(kv)) {
		line, err := RenderLine(k, kv[k])
		if err != nil {
			refused = append(refused, k)
			continue
		}
		lines = append(lines, line)
	}
	return lines, refused
}

// RenderShellValue returns v in a form that a POSIX shell or zsh (`eval`)
// and the v1 readers both read back as v. A value made only of characters
// the shell treats literally, and not starting with '=', is left bare. Otherwise it is single-quoted when it has no
// single quote, and double-quoted when it has none of `"`, `\`, `$` or a
// backtick, the characters a shell interprets inside double quotes. Anything
// else is refused.
func RenderShellValue(v string) (string, error) {
	if strings.Contains(v, "\n") {
		return "", ErrNewline
	}
	// Command substitution drops NUL bytes before eval sees them.
	if strings.ContainsRune(v, 0) {
		return "", ErrShellUnsafe
	}
	if shellBare(v) {
		return v, nil
	}
	return quote(v, "\"\\$`", ErrShellUnsafe)
}

func quote(v, dqForbidden string, refusal error) (string, error) {
	if !strings.Contains(v, "'") {
		return "'" + v + "'", nil
	}
	if !strings.ContainsAny(v, dqForbidden) {
		return `"` + v + `"`, nil
	}
	return "", refusal
}

// bareSafe reports whether v, written bare after KEY=, reads back as v: it
// must not start a continuation or look like a quoted value.
func bareSafe(v string) bool {
	if strings.HasSuffix(v, "\\") {
		return false
	}
	return stripQuotes(v) == v
}

func shellBare(v string) bool {
	// zsh expands a leading '=' in an assignment to a command path.
	if strings.HasPrefix(v, "=") {
		return false
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("_./:@%+,=-", c) >= 0:
		default:
			return false
		}
	}
	return true
}

// RenderShellLine returns the KEY=VALUE line for eval, without the newline.
// Besides refusing what RenderShellValue refuses, it refuses a key that bash
// or zsh treats specially on assignment: integer variables such as RANDOM
// evaluate the assigned text as arithmetic, which runs command substitutions
// even inside single quotes, and others change how the shell parses or
// prompts.
func RenderShellLine(key, v string) (string, error) {
	if !ValidKey(key) {
		return "", ErrInvalidKey
	}
	if shellReserved[key] {
		return "", ErrShellReserved
	}
	rv, err := RenderShellValue(v)
	if err != nil {
		return "", err
	}
	return key + "=" + rv, nil
}

// shellReserved holds the variables bash (3.2 through 5) and zsh evaluate or
// act on when assigned: their integer specials, plus the ones that steer
// parsing, prompts and startup.
var shellReserved = func() map[string]bool {
	m := map[string]bool{}
	for _, k := range strings.Fields(`
		ARGC BASHOPTS BASHPID BASH_ARGC BASH_ARGV BASH_COMMAND BASH_ENV
		BASH_LINENO BASH_SUBSHELL COLUMNS EGID ENV EPOCHREALTIME EPOCHSECONDS
		EUID FUNCNEST GID HISTCMD HISTFILESIZE HISTSIZE IFS KEYTIMEOUT LINENO
		LINES LISTMAX MAILCHECK OPTERR OPTIND PPID PROMPT PROMPT2 PROMPT3
		PROMPT4 PROMPT_COMMAND PS1 PS2 PS3 PS4 RANDOM RPROMPT RPS1 RPS2
		SAVEHIST SECONDS SHELLOPTS SHLVL SRANDOM TMOUT TRY_BLOCK_ERROR
		TRY_BLOCK_INTERRUPT TTYIDLE UID ZSH_SUBSHELL status`) {
		m[k] = true
	}
	return m
}()
