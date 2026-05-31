// Package chrome reads and writes Chrome cookies on macOS, handling the
// per-machine Safe Storage encryption via the macOS Keychain.
package chrome

import (
	"context"
	"crypto/pbkdf2"
	"crypto/sha1"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	keychainAccount = "Chrome"
	keychainService = "Chrome Safe Storage"
	pbkdf2Salt      = "saltysalt"
	pbkdf2Iter      = 1003
	aesKeyLen       = 16

	// SafeStorageRemediation is the operator-facing instruction shown when a
	// caller cannot read the Chrome Safe Storage key. The fix is the v0.13
	// one-password partition open, runnable entirely over SSH with no GUI
	// SecurityAgent prompt — NOT the obsolete "Always Allow in Keychain
	// Access" GUI click, which is unreachable on a headless sink.
	SafeStorageRemediation = "grant access over SSH with one login-password entry: 'agentcookie wizard set-keychain-access' (no GUI prompt needed; non-interactive form: AGENTCOOKIE_LOGIN_PASSWORD=… agentcookie wizard set-keychain-access)"

	// safeStorageReadTimeout caps how long the `security` CLI fallback
	// can block. On macOS, if the calling binary lacks ACL access AND a
	// GUI session is associated, `security find-generic-password` shows
	// a Keychain prompt and blocks until the user clicks. A headless
	// sink (LaunchAgent in the user's GUI session but no monitor or
	// active operator) leaves that prompt sitting forever, and the
	// sink daemon hangs before binding its listener -- the symptom
	// observed in the 2026-05-19 first-friend dry-run (#18). Fail loud
	// after this many seconds so the operator sees a clear error
	// instead of a silent hang.
	safeStorageReadTimeout = 10 * time.Second
)

// SafeStoragePassword returns the Chrome Safe Storage password from the macOS
// Keychain. On darwin+CGO builds, tries the keybase/go-keychain API path
// first (SecItemCopyMatching), then falls back to shelling out to the
// `security` CLI. The keybase path is more reliable from LaunchAgent
// contexts where the legacy `security` CLI sometimes returns empty output
// or non-zero exits despite the underlying keychain being readable.
//
// Either path requires the binary to be in the Keychain item's ACL OR the
// item to be marked any-app-accessible OR running in a context where the
// login keychain is auto-unlocked (LaunchAgents in the user GUI session).
// Plan 2026-05-17-004's wizard set-keychain-access step is what makes that
// last condition true for new binaries.
func SafeStoragePassword() (string, error) {
	if pw, err := safeStoragePasswordViaKeybase(); err == nil {
		return pw, nil
	}
	// Fall back to `security` CLI shell-out, bounded by a timeout so a
	// hung GUI Keychain prompt fails loud instead of blocking forever.
	ctx, cancel := context.WithTimeout(context.Background(), safeStorageReadTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "security",
		"find-generic-password",
		"-a", keychainAccount,
		"-s", keychainService,
		"-w",
	)
	out, err := cmd.Output()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "", fmt.Errorf("read Chrome Safe Storage from Keychain timed out after %s; this binary is not yet in the Safe Storage partition. %s", safeStorageReadTimeout, SafeStorageRemediation)
	}
	if err != nil {
		return "", fmt.Errorf("read Chrome Safe Storage from Keychain (did you grant access?): %w", err)
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// DeriveAESKey turns the Safe Storage password into the AES-128 key Chrome uses
// for cookie value encryption on this machine. Salt and iteration count are
// pinned to Chrome's macOS implementation.
func DeriveAESKey(password string) ([]byte, error) {
	key, err := pbkdf2.Key(sha1.New, password, []byte(pbkdf2Salt), pbkdf2Iter, aesKeyLen)
	if err != nil {
		return nil, fmt.Errorf("pbkdf2: %w", err)
	}
	return key, nil
}

// DefaultPartitionList is the partition string SetSafeStoragePartitionList
// uses when its argument is empty. Apple-signed binaries and Apple-tool
// callers (like the `security` CLI) read Chrome Safe Storage without a GUI
// prompt under this list. Ad-hoc-signed Go binaries (most PP CLIs) still
// need a one-time "Always Allow" click on first read; the partition list
// is groundwork, not a complete grant.
const DefaultPartitionList = "apple-tool:,apple:"

// TeamPartitionList composes the partition string that grants Chrome Safe
// Storage access to the `security` CLI (apple-tool:), Apple-signed system
// binaries (apple:), and Developer-ID-signed binaries from teamID
// (teamid:<teamID>). The teamid entry is what covers agentcookie's own
// CGO read path (SecItemCopyMatching) and any tool the operator signs with
// the same Developer ID team.
//
// When teamID is empty (the running binary is ad-hoc/unsigned, so no team
// can be resolved) this falls back to DefaultPartitionList: the security
// CLI cookie tools (yt-dlp, pycookiecheat, browser_cookie3, gallery-dl)
// are still covered, but Dev-ID-signed CGO readers are not. Callers should
// warn in that case rather than silently narrowing.
func TeamPartitionList(teamID string) string {
	if teamID == "" {
		return DefaultPartitionList
	}
	return DefaultPartitionList + ",teamid:" + teamID
}

// codesignRunner indirects the codesign invocation so BinaryTeamID is
// testable without a signed binary on disk. codesign writes its signing
// information to stderr, so production uses CombinedOutput.
var codesignRunner = func(path string) (string, error) {
	out, err := exec.Command("/usr/bin/codesign", "-d", "--verbose=2", path).CombinedOutput()
	return string(out), err
}

// BinaryTeamID resolves the Developer ID Team Identifier from a binary's
// code signature by parsing codesign's `TeamIdentifier=` line. It returns
// ("", nil) — not an error — when the binary is ad-hoc/unsigned or carries
// no team ("TeamIdentifier=not set"), so callers can fall back to the
// team-less partition list cleanly. A non-nil error is reserved for a
// genuine codesign execution failure where the team is truly unknown.
func BinaryTeamID(path string) (string, error) {
	out, err := codesignRunner(path)
	team := parseTeamIdentifier(out)
	if team != "" {
		return team, nil
	}
	// An unsigned / ad-hoc binary makes codesign exit non-zero ("code object
	// is not signed at all") yet there is simply no team to resolve — treat
	// that as a clean ("", nil) fallback rather than a hard error.
	if err == nil || isUnsignedCodesignOutput(out) {
		return "", nil
	}
	return "", fmt.Errorf("resolve team id of %s via codesign: %w", path, err)
}

// isUnsignedCodesignOutput reports whether codesign's output indicates the
// binary simply carries no signature (vs. a genuine tool/exec failure).
func isUnsignedCodesignOutput(out string) bool {
	lc := strings.ToLower(out)
	return strings.Contains(lc, "not signed") || strings.Contains(lc, "code object is not signed")
}

// parseTeamIdentifier extracts the value of the `TeamIdentifier=` line from
// codesign's verbose output. Returns "" for a missing line or the literal
// "not set" sentinel codesign emits for ad-hoc signatures.
func parseTeamIdentifier(codesignOutput string) string {
	for _, line := range strings.Split(codesignOutput, "\n") {
		line = strings.TrimSpace(line)
		const prefix = "TeamIdentifier="
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		val := strings.TrimSpace(strings.TrimPrefix(line, prefix))
		if val == "" || val == "not set" {
			return ""
		}
		return val
	}
	return ""
}

// buildPartitionListArgv returns the argv for the `security` command that
// updates the Chrome Safe Storage partition list. Split out from
// SetSafeStoragePartitionList so the argv shape is unit-testable without
// shelling out.
//
// When loginPassword is non-empty it is inserted as a discrete `-k <pw>`
// argument: this both authorizes the ACL change and unlocks the login
// keychain for the single call, which is what makes the partition update
// succeed over SSH (where the login keychain is otherwise locked). An empty
// loginPassword omits `-k` entirely (no empty `-k ""` element), preserving
// the legacy stdin/GUI behavior.
func buildPartitionListArgv(partitions, loginPassword string) []string {
	if partitions == "" {
		partitions = DefaultPartitionList
	}
	argv := []string{
		"set-generic-password-partition-list",
		"-S", partitions,
	}
	if loginPassword != "" {
		argv = append(argv, "-k", loginPassword)
	}
	argv = append(argv,
		"-s", keychainService,
		"-a", keychainAccount,
	)
	return argv
}

// SetSafeStoragePartitionList expands the partition list on the Chrome
// Safe Storage Keychain item so headless callers can read it with no GUI
// prompt. macOS prompts the user for their login keychain password the
// first time this runs; subsequent runs with the same list are no-ops.
// Idempotent.
//
// Passing partitions = "" uses DefaultPartitionList. Ad-hoc-signed
// binaries (most Go CLIs) are NOT covered by the default and still need
// their own one-time Always Allow click on first read; the partition list
// covers Apple-tool intermediaries (e.g., the `security` CLI itself).
func SetSafeStoragePartitionList(partitions string) error {
	argv := buildPartitionListArgv(partitions, "")
	cmd := exec.Command("security", argv...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("set Chrome Safe Storage partition list (login keychain password required): %w", err)
	}
	return nil
}

// partitionListRunner indirects the `security set-generic-password-partition-list`
// shell-out so the password-authenticated path is testable without touching
// the real Keychain or echoing a password through a shared process.
var partitionListRunner = func(argv []string) (string, error) {
	out, err := exec.Command("security", argv...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// SetSafeStoragePartitionListWithPassword updates the Chrome Safe Storage
// partition list, supplying the operator's login keychain password via the
// `-k` argument so the call succeeds non-interactively and over SSH with no
// GUI SecurityAgent prompt. This is the SSH-safe primary onboarding path.
//
// It performs NO delete and NO rewrite of the Safe Storage item — only its
// access partition list is changed — so the encryption key value is
// structurally untouched and existing Chrome cookies stay decryptable.
//
// SECURITY: loginPassword is passed as a single discrete argv element and is
// never logged, persisted, or echoed by this function. The caller owns
// zeroing it after the call. (The password is briefly visible in `ps` for
// the lifetime of the `security` call, which is unavoidable for `security -k`.)
func SetSafeStoragePartitionListWithPassword(partitions, loginPassword string) error {
	if loginPassword == "" {
		return fmt.Errorf("set Chrome Safe Storage partition list: login password required for the SSH-safe (-k) path")
	}
	argv := buildPartitionListArgv(partitions, loginPassword)
	if detail, err := partitionListRunner(argv); err != nil {
		// Do not include argv in the error — it carries the password.
		return fmt.Errorf("set Chrome Safe Storage partition list: %w (%s)", err, redactPassword(detail, loginPassword))
	}
	return nil
}

// redactPassword removes the login password from any diagnostic text before
// it reaches a log or error, in case `security` ever echoes it back.
func redactPassword(s, password string) string {
	if password == "" {
		return s
	}
	return strings.ReplaceAll(s, password, "<redacted>")
}

// dumpKeychainRunner indirects `security dump-keychain` so CountSafeStorageItems
// is testable without the real Keychain. dump-keychain lists item METADATA only
// (service/account attributes, never the secret values), so it succeeds over
// SSH without unlocking the login keychain — which is what lets doctor detect
// the duplicate-item race from a locked SSH session.
var dumpKeychainRunner = func() (string, error) {
	out, err := exec.Command("/usr/bin/security", "dump-keychain").CombinedOutput()
	return string(out), err
}

// safeStorageItemMarker is the dump-keychain line that appears exactly once per
// Chrome Safe Storage generic-password item (the service attribute). Counting
// these counts items.
const safeStorageItemMarker = `"svce"<blob>="Chrome Safe Storage"`

// CountSafeStorageItems returns how many Chrome Safe Storage keychain items
// exist. Exactly one is healthy. More than one is the install-time
// Chrome-relaunch race signature: the sink daemon's CDP injector relaunches
// Chrome, Chrome recreates its own competing item, and now a partition set on
// one item while a reader hits another diverge. Zero means Chrome has never
// written its Safe Storage key on this box.
func CountSafeStorageItems() (int, error) {
	out, err := dumpKeychainRunner()
	if out == "" && err != nil {
		return 0, fmt.Errorf("dump keychain: %w", err)
	}
	n := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, safeStorageItemMarker) {
			n++
		}
	}
	return n, nil
}

// IsKeychainLocked reports whether err is the macOS "User interaction is not
// allowed" (-25308) failure. Over SSH that means the login keychain is locked,
// NOT that the caller lacks ACL/partition access: the GUI-session daemon reads
// the very same item fine, and a logged-in desktop Mac is unlocked. Callers use
// this to avoid treating a locked-SSH read as "the grant failed" and to avoid
// advising a destructive re-open when nothing is actually wrong with the grant.
func IsKeychainLocked(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "25308") || strings.Contains(s, "interaction is not allowed")
}
