package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mvanhorn/agentcookie/internal/chrome"
)

var (
	setKeychainExtraBinary []string
	innerRunnerMode        bool
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
	return runOuterWizard(cmd)
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
	strategies := buildStrategies(setKeychainExtraBinary)

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

func buildStrategies(extraBinaries []string) []kcStrategy {
	out := []kcStrategy{
		{
			name: "partition-list:apple-tool,apple",
			apply: func() (string, error) {
				return execSecurity("set-generic-password-partition-list",
					"-S", "apple-tool:,apple:",
					"-s", "Chrome Safe Storage", "-a", "Chrome")
			},
		},
		{
			name: "partition-list:apple-tool,apple,teamid",
			apply: func() (string, error) {
				return execSecurity("set-generic-password-partition-list",
					"-S", "apple-tool:,apple:,teamid:",
					"-s", "Chrome Safe Storage", "-a", "Chrome")
			},
		},
	}

	for _, bin := range extraBinaries {
		bin := bin
		out = append(out, kcStrategy{
			name: "trust-list:" + filepath.Base(bin),
			apply: func() (string, error) {
				// Update existing item: preserve password, add this binary to trust list.
				// Requires reading the current password first (which works from LaunchAgent
				// context because keychain is unlocked).
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
func execSecurity(args ...string) (string, error) {
	cmd := exec.Command("/usr/bin/security", args...)
	out, err := cmd.CombinedOutput()
	detail := strings.TrimSpace(string(out))
	if err != nil {
		return detail, fmt.Errorf("security %s: %w (%s)", args[0], err, detail)
	}
	return detail, nil
}

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
