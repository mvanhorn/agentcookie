package cli

import (
	cryptorand "crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/keystore"
)

var (
	setKeychainExtraBinary    []string
	innerRunnerMode           bool
	setKeychainEnableSeal     bool
	setKeychainAnyApp         bool
	setKeychainLegacyRecreate bool
)

var setKeychainAccessCmd = &cobra.Command{
	Use:   "set-keychain-access",
	Short: "Broaden Chrome Safe Storage access so kooky-using CLIs read without per-binary prompts",
	Long: `set-keychain-access tries each of several strategies (partition-list
expansion, then per-binary trust-list additions) and lands on the first
that lets kooky-CGO callers (instacart-pp-cli, bird, future PP CLIs)
read Chrome Safe Storage silently from a LaunchAgent context.

The mutations themselves run inside a one-shot LaunchAgent that
agentcookie spawns. LaunchAgents run in the user's GUI session where
the login keychain is auto-unlocked, so no login password prompt fires
during the operation. Each strategy is followed by a probe (using the
same Keychain API path kooky-CGO uses) to verify the change actually
took.

If the wizard invokes this with no prior install on the box, the
default strategy chain is sufficient. Pass --extra-binary <absolute path>
(repeatable) to add specific kooky-using CLIs to the per-binary
trust-list fallback if the partition-list strategies are insufficient
on your macOS version.`,
	RunE: runSetKeychainAccess,
}

func init() {
	setKeychainAccessCmd.Flags().StringArrayVar(&setKeychainExtraBinary, "extra-binary", nil, "absolute path to a kooky-using CLI binary; added to the trust-list fallback if partition-list strategies do not cover it; repeatable")
	setKeychainAccessCmd.Flags().BoolVar(&innerRunnerMode, "inner-runner", false, "run the strategy loop in this process (used internally when invoked as a one-shot LaunchAgent); end users do not pass this")
	_ = setKeychainAccessCmd.Flags().MarkHidden("inner-runner")
	// --enable-sealing: hidden v0.12 flag that turns on at-rest sealing
	// of the sidecar SQLite and adapter session files. Off by default
	// because the PP CLI side of the sealing handshake (U12) has not
	// shipped yet; flipping this on without that work breaks PP CLI
	// reads. Will become default-on once U12 lands in cli-printing-press.
	setKeychainAccessCmd.Flags().BoolVar(&setKeychainEnableSeal, "enable-sealing", false, "create the agentcookie-master Keychain item and turn on at-rest sealing of the sidecar SQLite and adapter session files")
	_ = setKeychainAccessCmd.Flags().MarkHidden("enable-sealing")
	// --any-app: opt-in v0.10-style universal open. Recreates the Chrome
	// Safe Storage item with -A (any application) so ANY unmodified cookie
	// tool reads it with no per-binary trust list. Unlike the v0.10 code,
	// this preserves the existing Safe Storage value (read-then-reuse) so
	// existing Chrome cookies stay decryptable. SECURITY: any local process
	// can then read Chrome cookies; only appropriate on a dedicated sink.
	setKeychainAccessCmd.Flags().BoolVar(&setKeychainAnyApp, "any-app", false, "recreate Chrome Safe Storage with -A (any application) so any unmodified cookie tool reads it; preserves the existing key value; SECURITY: any local process can then read Chrome cookies")
	// --recreate: opt into the legacy v0.12 LaunchAgent delete-and-recreate
	// strategy chain. The v0.13 default is the inline one-password
	// partition-list path (no LaunchAgent, no GUI prompt-storm). This flag
	// is retained for the dedicated-sink / unsigned-CGO long tail where the
	// partition path is insufficient.
	setKeychainAccessCmd.Flags().BoolVar(&setKeychainLegacyRecreate, "recreate", false, "use the legacy LaunchAgent delete-and-recreate trust-list strategy instead of the default inline one-password partition-list path")
	wizardCmd.AddCommand(setKeychainAccessCmd)
}

// strategyOutcome is the structured result one strategy attempt yields.
// JSON-encoded by the inner runner so the outer wizard caller can parse
// without re-parsing free-form text.
type strategyOutcome struct {
	Name      string `json:"name"`
	Success   bool   `json:"success"`
	Detail    string `json:"detail,omitempty"`
	ProbeLen  int    `json:"probe_len,omitempty"`
	ErrorText string `json:"error,omitempty"`
}

// runResult is what the inner runner emits as its final JSON line.
type runResult struct {
	WinningStrategy string            `json:"winning_strategy,omitempty"`
	Attempts        []strategyOutcome `json:"attempts"`
	OverallSuccess  bool              `json:"overall_success"`
}

func runSetKeychainAccess(cmd *cobra.Command, args []string) error {
	if innerRunnerMode {
		return runInnerStrategyLoop(cmd)
	}
	switch keychainStrategyMode(setKeychainAnyApp, setKeychainLegacyRecreate) {
	case keychainModeRecreate:
		// The legacy recreate chain benefits from the default kooky-using
		// CLI trust list when the operator did not name binaries explicitly.
		if len(setKeychainExtraBinary) == 0 {
			setKeychainExtraBinary = defaultKeychainTrustBinaries()
		}
		return runOuterWizard(cmd)
	default:
		return runInlinePartitionAccess()
	}
}

// keychain strategy routing modes.
const (
	keychainModeInline   = "inline"
	keychainModeRecreate = "recreate"
)

// keychainStrategyMode resolves which onboarding strategy runs. The v0.13
// default is the inline one-password partition path; --any-app and
// --recreate both opt into the legacy LaunchAgent delete-and-recreate chain.
// Pure function so the routing is unit-testable.
func keychainStrategyMode(anyApp, legacyRecreate bool) string {
	if anyApp || legacyRecreate {
		return keychainModeRecreate
	}
	return keychainModeInline
}

// Seams for the inline partition path so the wizard logic is testable
// without touching the real Keychain. Production wires them to the chrome
// package; tests stub them.
var (
	resolveTeamFunc         = chrome.BinaryTeamID
	setPartitionWithPwFunc  = chrome.SetSafeStoragePartitionListWithPassword
	verifyPartitionReadFunc = verifyPartitionRead
)

// runInlinePartitionAccess is the v0.13 default keychain-access strategy. It
// resolves the running binary's Developer ID team, prompts for the login
// password once (or reads AGENTCOOKIE_LOGIN_PASSWORD), and sets the Chrome
// Safe Storage partition list with that password via `security -k`. It
// performs NO delete and NO LaunchAgent dispatch, so it runs cleanly over
// SSH with a single password entry and no GUI SecurityAgent prompt, and
// never touches the Safe Storage key value.
func runInlinePartitionAccess() error {
	self := agentcookieBinaryPath()
	if self == "" {
		return fmt.Errorf("cannot resolve the agentcookie binary path to determine its Developer ID team")
	}
	team, err := resolveTeamFunc(self)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agentcookie wizard: WARNING could not resolve Developer ID team (%v); the partition will cover security-CLI cookie tools only\n", err)
	}
	if team == "" {
		fmt.Fprintln(os.Stderr, "agentcookie wizard: WARNING this agentcookie binary is not Developer-ID-signed; the partition covers security-CLI cookie tools (apple-tool) but not Dev-ID CGO readers. The sink daemon needs a signed binary to read via teamid.")
	}

	pw, err := acquireLoginPasswordFunc()
	if err != nil {
		return err
	}

	// Race fix (U2): before granting access, collapse any duplicate Chrome
	// Safe Storage items down to one. Duplicates are the install-time
	// Chrome-relaunch race signature -- a partition set on one item while a
	// reader hits another. No-op on the healthy single-item path, so the
	// proven happy path is unchanged; only a contaminated keychain triggers
	// the value-preserved collapse, which refuses if the value can't be read.
	if removed, cerr := convergeSafeStorageToOneItem(pw); cerr != nil {
		return fmt.Errorf("converge duplicate Chrome Safe Storage items: %w", cerr)
	} else if removed > 0 {
		fmt.Fprintf(os.Stderr, "agentcookie wizard: collapsed %d duplicate Chrome Safe Storage item(s) to one (existing key value preserved) before granting access\n", removed)
	}

	partitions := chrome.TeamPartitionList(team)
	if err := setPartitionWithPwFunc(partitions, pw); err != nil {
		return fmt.Errorf("set Chrome Safe Storage partition list: %w", err)
	}

	if verr := verifyPartitionReadFunc(); verr != nil {
		fmt.Fprintf(os.Stderr, "agentcookie wizard: partition set (%s); a verification read from this session did not succeed (%v) -- this is expected over SSH where the login keychain re-locks; the daemon reads it in the GUI session.\n", partitions, verr)
	} else {
		fmt.Fprintf(os.Stderr, "agentcookie wizard: Chrome Safe Storage partition set and verified readable (%s); universal delivery enabled with no GUI prompt.\n", partitions)
	}
	return nil
}

// Seams for the converge step so its delete/re-add logic is unit-testable
// without touching the real Keychain.
var (
	countSafeStorageItemsFunc = chrome.CountSafeStorageItems
	unlockKeychainFunc        = func(pw string) error {
		if pw == "" {
			return nil // GUI session is already unlocked; nothing to do.
		}
		_, err := execSecurityFunc("unlock-keychain", "-p", pw)
		return err
	}
)

// convergeSafeStorageToOneItem collapses duplicate Chrome Safe Storage items
// to exactly one, preserving the existing key value. Returns the number of
// duplicate items removed (0 when already single or absent -- the common,
// proven path, where this is a pure no-op).
//
// Duplicates are the install-time Chrome-relaunch race: the sink daemon's CDP
// injector relaunches Chrome, Chrome recreates its own competing item, and now
// a partition set on one item while a reader hits another diverge.
//
// COOKIE-SAFETY (KTD3/KTD4): it reads the existing value FIRST and refuses to
// delete anything if the value cannot be read, because recreating the item
// with a different value would permanently destroy all existing Chrome cookies
// (Chromium derives its AES key from this value via PBKDF2). The surviving item
// is recreated with the SAME value, never a random one. This mirrors the proven
// read-then-reuse guard in the --any-app strategy above.
func convergeSafeStorageToOneItem(loginPassword string) (removed int, err error) {
	n, err := countSafeStorageItemsFunc()
	if err != nil {
		return 0, fmt.Errorf("count items: %w", err)
	}
	if n <= 1 {
		return 0, nil // healthy (or no key yet) -- nothing to collapse.
	}

	// Reading the existing value over SSH needs an unlocked login keychain;
	// unlock with the same password used for the partition set. In the GUI
	// session (empty password) the keychain is already unlocked.
	if uerr := unlockKeychainFunc(loginPassword); uerr != nil {
		return 0, fmt.Errorf("unlock login keychain to read existing value: %w", uerr)
	}
	existing, rerr := safeStoragePasswordFunc()
	if rerr != nil {
		// Refuse-to-delete guard: never collapse when we cannot preserve the
		// value, or we would wipe every existing cookie.
		return 0, fmt.Errorf("cannot read existing Chrome Safe Storage value (%w); refusing to collapse %d duplicate item(s) because recreating with a different value would permanently destroy all existing Chrome cookies. Grant this binary read access once, then re-run", rerr, n)
	}

	// Delete every matching item, then re-add exactly one with the SAME value.
	for i := 0; i < n; i++ {
		if _, derr := execSecurityFunc("delete-generic-password",
			"-s", "Chrome Safe Storage", "-a", "Chrome"); derr != nil {
			break // nothing left to delete
		}
	}
	if _, aerr := execSecurityFunc("add-generic-password",
		"-s", "Chrome Safe Storage", "-a", "Chrome", "-w", existing); aerr != nil {
		return 0, fmt.Errorf("re-add single Chrome Safe Storage item with preserved value: %w", aerr)
	}
	return n - 1, nil
}

// verifyPartitionRead confirms the Safe Storage item is readable via the
// `security` CLI (now covered by the apple-tool partition). It deliberately
// discards the read value (the secret) and only reports readability.
func verifyPartitionRead() error {
	if _, err := execSecurityFunc("find-generic-password",
		"-s", "Chrome Safe Storage", "-a", "Chrome", "-w"); err != nil {
		return err
	}
	return nil
}

// runOuterWizard is the user-facing path. It writes a one-shot LaunchAgent
// that re-invokes this binary with --inner-runner, then parses the
// resulting JSON to report what happened.
func runOuterWizard(cmd *cobra.Command) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate this binary: %w", err)
	}
	exe, _ = filepath.Abs(exe)

	argv := []string{exe, "wizard", "set-keychain-access", "--inner-runner"}
	for _, b := range setKeychainExtraBinary {
		argv = append(argv, "--extra-binary", b)
	}
	if setKeychainEnableSeal {
		argv = append(argv, "--enable-sealing")
	}
	if setKeychainAnyApp {
		argv = append(argv, "--any-app")
	}

	fmt.Fprintln(os.Stderr, "agentcookie wizard: running keychain strategies via a one-shot LaunchAgent (no prompts expected; if a Mac mini desktop prompt appears, click Always Allow and re-run)")

	res, err := chrome.RunOneShotLaunchAgent(argv, 30*time.Second)
	if err != nil {
		return fmt.Errorf("LaunchAgent dispatch: %w (stdout=%q stderr=%q)", err, res.Stdout, res.Stderr)
	}

	// The inner runner's last stdout line is a JSON runResult.
	var parsed runResult
	if line := lastJSONLine(res.Stdout); line != "" {
		if err := json.Unmarshal([]byte(line), &parsed); err != nil {
			return fmt.Errorf("parse inner runner output (raw=%q): %w", res.Stdout, err)
		}
	} else {
		return fmt.Errorf("inner runner produced no JSON output (stdout=%q stderr=%q exit=%d)", res.Stdout, res.Stderr, res.Exit)
	}

	for _, a := range parsed.Attempts {
		if a.Success {
			fmt.Fprintf(os.Stderr, "agentcookie wizard:   strategy %q -> success (probe len=%d)\n", a.Name, a.ProbeLen)
		} else {
			fmt.Fprintf(os.Stderr, "agentcookie wizard:   strategy %q -> failed (%s)\n", a.Name, a.ErrorText)
		}
	}

	if parsed.OverallSuccess {
		fmt.Fprintf(os.Stderr, "agentcookie wizard: keychain access: %s\n", parsed.WinningStrategy)
		return nil
	}
	return fmt.Errorf("keychain access: FAILED (all strategies exhausted; see attempt log above and docs/runbook-v0.10-keychain-access.md)")
}

// runInnerStrategyLoop is invoked inside the one-shot LaunchAgent, where
// the login keychain is unlocked. Iterates strategies, probes after
// each, emits structured JSON on stdout for the outer wizard to parse.
func runInnerStrategyLoop(cmd *cobra.Command) error {
	strategies := buildStrategies(setKeychainExtraBinary, setKeychainEnableSeal, setKeychainAnyApp)

	var result runResult
	for _, s := range strategies {
		outcome := tryStrategy(s)
		result.Attempts = append(result.Attempts, outcome)
		if outcome.Success {
			result.WinningStrategy = outcome.Name
			result.OverallSuccess = true
			break
		}
	}

	// Emit JSON for the outer caller. Last line on stdout is parsed.
	out, _ := json.Marshal(result)
	fmt.Println(string(out))
	if !result.OverallSuccess {
		return fmt.Errorf("no strategy succeeded")
	}
	return nil
}

type kcStrategy struct {
	name  string
	apply func() (detail string, err error)
}

func buildStrategies(extraBinaries []string, enableSealing bool, anyApp bool) []kcStrategy {
	// Trust list: the running agentcookie binary plus every adapter
	// binary in extraBinaries. Master key + Chrome Safe Storage ACLs
	// both use this same list so any allowlisted process can read
	// either secret.
	trustList := agentcookieBinaryPath()
	trustArgs := []string{"-T", trustList}
	for _, b := range extraBinaries {
		ab, _ := filepath.Abs(b)
		if ab == "" {
			continue
		}
		trustArgs = append(trustArgs, "-T", ab)
	}

	var out []kcStrategy

	if anyApp {
		// Opt-in v0.10-style universal open, corrected to preserve the
		// existing Safe Storage value. This leads the chain when
		// --any-app is set so that any unmodified cookie tool (yt-dlp,
		// kooky, pycookiecheat, a random third-party CLI) reads Chrome
		// Safe Storage with no per-binary trust list.
		//
		// CRITICAL correctness guard: we read the existing value FIRST.
		// If that read fails, the caller is not yet in the item's ACL,
		// and we MUST NOT delete the item: deleting then recreating with
		// a different value permanently destroys all existing Chrome
		// cookies (Chromium derives its AES key from this value via
		// PBKDF2). On a read failure we refuse to delete and return the
		// one-time GUI-grant instruction instead.
		out = append(out, kcStrategy{
			name: "delete-and-recreate-with-A",
			apply: func() (string, error) {
				existing, err := safeStoragePasswordFunc()
				if err != nil {
					// Refuse-to-delete guard: caller not yet trusted in
					// the Chrome Safe Storage ACL. Do NOT delete anything.
					return "", fmt.Errorf("cannot read existing Chrome Safe Storage value (%w); refusing to delete the item because recreating it with a different value would permanently destroy all existing Chrome cookies. Do the one-time \"Always Allow\" grant in Keychain Access (see docs/runbook-v0.10-keychain-access.md), then re-run", err)
				}
				// Value read OK. Safe to delete and recreate with -A and
				// the SAME value (never a random one), so existing cookies
				// stay decryptable.
				_, _ = execSecurityFunc("delete-generic-password",
					"-s", "Chrome Safe Storage", "-a", "Chrome")
				args := []string{
					"add-generic-password",
					"-s", "Chrome Safe Storage", "-a", "Chrome",
					"-w", existing,
					"-A",
				}
				if detail, err := execSecurityFunc(args...); err != nil {
					return detail, err
				}
				return "Chrome Safe Storage recreated with -A (any application) and the existing key value preserved. SECURITY WARNING: any local process can now read Chrome cookies; only appropriate on a dedicated agent sink.", nil
			},
		})
	}

	out = append(out, []kcStrategy{
		{
			// Primary v0.12 strategy: delete the existing Chrome Safe
			// Storage item, recreate with a per-binary -T trust list
			// instead of the v0.10 -A (any-app). The agentcookie binary
			// and each adapter binary are named explicitly. Because the
			// agentcookie binary carries a Developer ID Application
			// designated requirement, the ACL stays valid across
			// rebuilds with the same identity.
			//
			// When enableSealing is true, the strategy also installs the
			// agentcookie-master Keychain item that the sidecar and
			// adapter writers use for at-rest sealing. Off by default
			// in v0.12 because the PP CLI consumer-side of sealing
			// (U12) has not shipped; turning sealing on without that
			// would break v0.11 PP CLIs that read plaintext.
			name: "delete-and-recreate-with-T",
			apply: func() (string, error) {
				_, _ = execSecurityFunc("delete-generic-password",
					"-s", "Chrome Safe Storage", "-a", "Chrome")
				pw := randomKeychainPassword()
				args := append([]string{
					"add-generic-password",
					"-s", "Chrome Safe Storage", "-a", "Chrome",
					"-w", pw,
				}, trustArgs...)
				if detail, err := execSecurityFunc(args...); err != nil {
					return detail, err
				}
				if !enableSealing {
					return "Chrome Safe Storage installed with -T allowlist (sealing not enabled; pass --enable-sealing once PP CLI U12 ships)", nil
				}
				if err := createMasterKeyFunc(trustList, extraBinaries); err != nil {
					return "", fmt.Errorf("create agentcookie-master: %w", err)
				}
				return "Chrome Safe Storage + agentcookie-master installed with -T allowlist", nil
			},
		},
		{
			// Fallback diagnostic: partition-list expansion. Rarely
			// succeeds from a LaunchAgent on modern macOS; included so
			// the wizard output surfaces a clear "this strategy did
			// not apply" line when the primary strategy fails.
			name: "partition-list:apple-tool,apple",
			apply: func() (string, error) {
				return execSecurity("set-generic-password-partition-list",
					"-S", "apple-tool:,apple:",
					"-s", "Chrome Safe Storage", "-a", "Chrome")
			},
		},
	}...)

	for _, bin := range extraBinaries {
		bin := bin
		out = append(out, kcStrategy{
			name: "trust-list:" + filepath.Base(bin),
			apply: func() (string, error) {
				pw, err := chrome.SafeStoragePassword()
				if err != nil {
					return "", fmt.Errorf("read existing password: %w", err)
				}
				return execSecurity("add-generic-password",
					"-s", "Chrome Safe Storage", "-a", "Chrome",
					"-w", pw, "-T", bin, "-U")
			},
		})
	}

	return out
}

// agentcookieBinaryPath returns the absolute path of the running
// agentcookie binary, resolved through symlinks. Used as the primary
// -T target so the v0.12 trust list pins the agentcookie sink alone.
// Returns the empty string on a resolution failure; callers treat
// that as a fatal install error rather than guessing.
func agentcookieBinaryPath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		// EvalSymlinks failing usually means a missing target on the
		// filesystem; fall back to the unresolved path rather than
		// returning empty.
		return exe
	}
	return resolved
}

// randomKeychainPassword returns 16 random bytes base64-encoded (~22 chars).
// Chrome uses a similar 16-byte random secret for its Safe Storage item;
// matching the shape keeps any future Chrome interop reasonable.
func randomKeychainPassword() string {
	b := make([]byte, 16)
	if _, err := cryptorand.Read(b); err != nil {
		// Fall back to a less-random but non-empty default. This branch is
		// essentially unreachable on darwin where crypto/rand is the system
		// RNG; the fallback exists so the caller never gets an empty password.
		return "agentcookie-fallback-secret"
	}
	return base64.RawStdEncoding.EncodeToString(b)
}

// tryStrategy applies one strategy, then probes via the keybase/go-keychain
// API path kooky-CGO uses. Returns a structured outcome.
func tryStrategy(s kcStrategy) strategyOutcome {
	outcome := strategyOutcome{Name: s.name}

	detail, err := s.apply()
	if err != nil {
		outcome.ErrorText = "apply: " + err.Error()
		return outcome
	}
	outcome.Detail = detail

	probeLen, perr := chrome.KeybaseKeychainProbe(5 * time.Second)
	if perr != nil {
		outcome.ErrorText = "probe: " + perr.Error()
		return outcome
	}
	outcome.ProbeLen = probeLen
	outcome.Success = true
	return outcome
}

// execSecurity runs /usr/bin/security with the given args, returns
// "stdout||stderr" as detail. Caller treats non-zero exit as failure.
//
// Indirection via execSecurityFunc lets tests inject a stub that records
// invocations without shelling out. Production code calls the real
// execSecurity via the function pointer.
var execSecurityFunc = execSecurity

func execSecurity(args ...string) (string, error) {
	cmd := exec.Command("/usr/bin/security", args...)
	out, err := cmd.CombinedOutput()
	detail := strings.TrimSpace(string(out))
	if err != nil {
		return detail, fmt.Errorf("security %s: %w (%s)", args[0], err, detail)
	}
	return detail, nil
}

// createMasterKeyFunc indirects keystore.CreateMasterKey so tests can
// observe whether the sealing-on path was taken without actually
// writing to the macOS Keychain.
var createMasterKeyFunc = keystore.CreateMasterKey

// safeStoragePasswordFunc indirects chrome.SafeStoragePassword so tests
// can inject a fake existing value (or a read failure) for the --any-app
// delete-and-recreate-with-A strategy without touching the real Keychain.
// The read-then-reuse guard depends on this seam to verify the recreate
// uses the exact value read and that a read failure refuses to delete.
var safeStoragePasswordFunc = chrome.SafeStoragePassword

func lastJSONLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		trim := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trim, "{") && strings.HasSuffix(trim, "}") {
			return trim
		}
	}
	return ""
}
