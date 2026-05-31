package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/chromepaths"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/keystore"
	"github.com/mvanhorn/agentcookie/internal/secretsbus"
	"github.com/mvanhorn/agentcookie/internal/sinkpush"
	"github.com/mvanhorn/agentcookie/internal/state"
	"github.com/mvanhorn/agentcookie/internal/tsclient"
)

// Severity tags a single doctor check. The OK/WARN/FAIL trio drive
// human output coloring and the overall exit code; INFO is reserved
// for purely informational lines; SKIPPED marks checks that do not
// apply on this role (e.g. sink-only checks on a source-only box).
type Severity string

const (
	SeverityOK      Severity = "ok"
	SeverityWarn    Severity = "warn"
	SeverityFail    Severity = "fail"
	SeverityInfo    Severity = "info"
	SeveritySkipped Severity = "skipped"
)

// Check is one row of a DoctorReport. Detail is the human-readable
// one-liner; Remediation names the concrete next step the user can
// run when Severity is FAIL or WARN.
type Check struct {
	Name        string   `json:"name"`
	Severity    Severity `json:"severity"`
	Detail      string   `json:"detail"`
	Remediation string   `json:"remediation,omitempty"`
}

// DoctorReport is the JSON envelope emitted by `agentcookie doctor --json`.
// The shape is part of the agent-facing contract; field names should
// not change without bumping the schema.
type DoctorReport struct {
	Version  string  `json:"version"`
	ExitCode int     `json:"exit_code"`
	Checks   []Check `json:"checks"`
}

// doctorDeps holds the system-surface dependencies the doctor checks
// need, injected for testability. Production callers fill in real
// implementations; tests substitute fakes.
type doctorDeps struct {
	ConfigDir       string
	BinarySignature func() (string, error)
	TailscaleIP     func() (string, error)
	LoadSourceState func() (*state.SourceState, error)
	LoadSinkState   func() (*state.SinkState, error)
	MasterKeyExists func() bool
}

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Run a self-check of the local agentcookie install and report OK / WARN / FAIL per check",
	Long: `doctor walks 8 health checks (binary signature, Tailscale, config,
keystore, sink listener, sink state, source state, sealing state) and
prints one line per check. Exit code is 0 only when no check FAILs.

Use --json to emit a stable machine-readable envelope. doctor opens
no network connections beyond local Tailscale daemon introspection;
it never phones home.

Typical run:
  agentcookie doctor

Run after install to confirm the box is healthy; run anytime later if
syncs look stuck.`,
	RunE: runDoctor,
}

func runDoctor(cmd *cobra.Command, args []string) error {
	home, _ := os.UserHomeDir()
	deps := doctorDeps{
		ConfigDir:       common.ConfigDir,
		BinarySignature: probeBinarySignature,
		TailscaleIP: func() (string, error) {
			return tsclient.RequireTailnetIP(context.Background())
		},
		LoadSourceState: func() (*state.SourceState, error) {
			return state.LoadSource(state.SourcePath(home))
		},
		LoadSinkState: func() (*state.SinkState, error) {
			return state.LoadSink(state.SinkPath(home))
		},
		MasterKeyExists: keystore.MasterKeyExists,
	}

	report := buildReport(deps)

	if common.JSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			return err
		}
		if report.ExitCode != 0 {
			os.Exit(report.ExitCode)
		}
		return nil
	}

	printHuman(os.Stdout, report)
	if report.ExitCode != 0 {
		os.Exit(report.ExitCode)
	}
	return nil
}

// buildReport runs all eight checks in order and computes the exit code.
// Pure function over doctorDeps so tests don't need a real Tailscale
// daemon, codesign binary, or filesystem layout outside the temp dir.
func buildReport(d doctorDeps) DoctorReport {
	checks := []Check{}

	// 1. Binary identity.
	checks = append(checks, checkBinarySignatureWith(d.BinarySignature))

	// 2. Tailscale.
	checks = append(checks, checkTailscaleWith(d.TailscaleIP))

	// 3. Config.
	configCheck, srcCfg, sinkCfg := checkConfigLoaded(d.ConfigDir)
	checks = append(checks, configCheck)

	// 4. Keystore: union of source/sink peer hostnames.
	peers := []string{}
	if srcCfg != nil && srcCfg.Peer.Hostname != "" {
		peers = append(peers, srcCfg.Peer.Hostname)
	}
	if sinkCfg != nil && sinkCfg.Peer.Hostname != "" {
		peers = append(peers, sinkCfg.Peer.Hostname)
	}
	checks = append(checks, checkKeystore(d.ConfigDir, peers))

	// 5. Sink listener -- sink role only.
	if sinkCfg != nil {
		checks = append(checks, checkSinkListener(sinkCfg.Listen.Addr))
	} else {
		checks = append(checks, Check{
			Name:     "Sink listener",
			Severity: SeveritySkipped,
			Detail:   "source-only install",
		})
	}

	// 6. Sink state -- sink role only.
	if sinkCfg != nil {
		st, err := d.LoadSinkState()
		checks = append(checks, checkSinkStateFrom(st, err))
	} else {
		checks = append(checks, Check{
			Name:     "Sink state",
			Severity: SeveritySkipped,
			Detail:   "source-only install",
		})
	}

	// 7. Source state -- source role only.
	if srcCfg != nil {
		st, err := d.LoadSourceState()
		checks = append(checks, checkSourceStateFrom(st, err))
		checks = append(checks, checkDBSCFrom(st))
	} else {
		checks = append(checks, Check{
			Name:     "Source state",
			Severity: SeveritySkipped,
			Detail:   "sink-only install",
		})
	}

	// 8. Sealing -- sink role only.
	if sinkCfg != nil {
		checks = append(checks, checkSealingWith(d.MasterKeyExists))
	} else {
		checks = append(checks, Check{
			Name:     "Sealing",
			Severity: SeveritySkipped,
			Detail:   "source-only install",
		})
	}

	// 9. Adapter coverage (v0.12.0-beta.3) -- sink role only. Surfaces
	// the risk that a configured sink syncs cookie domains that no
	// adapter writes and no sidecar-reading PP CLI consumes. WARN, not
	// FAIL, because the sidecar always covers sidecar-aware callers; the
	// gap is for kooky-only readers that don't link pkg/sidecar.
	if sinkCfg != nil {
		checks = append(checks, checkAdapterCoverage(sinkCfg))
	} else {
		checks = append(checks, Check{
			Name:     "Adapter coverage",
			Severity: SeveritySkipped,
			Detail:   "source-only install",
		})
	}

	// 10. CDP injector (v0.12.0-beta.3) -- sink role only. Verifies the
	// CDP profile dir exists when cdp.enabled, and Chrome.app is
	// installed on this Mac. WARN when configured but unusable.
	if sinkCfg != nil {
		checks = append(checks, checkCDPInjector(sinkCfg))
	} else {
		checks = append(checks, Check{
			Name:     "CDP injector",
			Severity: SeveritySkipped,
			Detail:   "source-only install",
		})
	}

	// 11. Secrets bus (v0.13). Reports how many CLIs are registered,
	// total key count, sealed-vs-plaintext mode, sync freshness.
	// Reads from the secrets root on whichever machine is running
	// doctor (both source and sink populate the same path; source
	// writes via `agentcookie secret`, sink writes via U4's writer).
	checks = append(checks, checkSecretsBus())

	// 12. Secret coverage. Flags CLIs whose synced secret store does not
	// provide the auth env var the CLI reads (e.g. store has OAUTH_BEARER
	// but the CLI reads TESLA_AUTH_TOKEN). WARN, recoverable via alias.
	checks = append(checks, checkSecretCoverage())

	// 13. Binary install. Flags multiple diverging agentcookie binaries so
	// the on-PATH copy and the daemon's copy don't silently differ.
	checks = append(checks, checkBinaryInstall())

	// 14. Cookie delivery (v0.13 universal cookie delivery) -- sink role
	// only. Tells the operator whether ANY unmodified cookie CLI works on
	// this box (universal) vs only agentcookie-aware tools (degraded).
	if sinkCfg != nil {
		checks = append(checks, checkCookieDelivery(sinkCfg))
	} else {
		checks = append(checks, Check{
			Name:     "Cookie delivery",
			Severity: SeveritySkipped,
			Detail:   "source-only install",
		})
	}

	exit := 0
	for _, c := range checks {
		if c.Severity == SeverityFail {
			exit = 1
			break
		}
	}

	return DoctorReport{
		Version:  Version,
		ExitCode: exit,
		Checks:   checks,
	}
}

// checkSecretCoverage flags CLIs whose synced secret store does not provide
// the auth env var the CLI reads (the Tesla case: store has OAUTH_BEARER but
// the CLI reads TESLA_AUTH_TOKEN). WARN, not FAIL: it is a real but
// recoverable misconfiguration the operator fixes with `secret alias`.
func checkSecretCoverage() Check {
	home, _ := os.UserHomeDir()
	reg, _ := secretsbus.Discover(secretsbus.DiscoveryConfig{HomeDir: home})
	if reg == nil || len(reg.Projects) == 0 {
		return Check{Name: "Secret coverage", Severity: SeverityOK, Detail: "no secrets-bus CLIs registered"}
	}
	var mismatches []string
	for name, rp := range reg.Projects {
		if status, _ := secretCoverage(name, declaredKeysOf(rp)); status == "MISMATCH" {
			mismatches = append(mismatches, name)
		}
	}
	if len(mismatches) == 0 {
		return Check{Name: "Secret coverage", Severity: SeverityOK, Detail: "synced secrets match the auth env var each CLI reads"}
	}
	sort.Strings(mismatches)
	return Check{
		Name:        "Secret coverage",
		Severity:    SeverityWarn,
		Detail:      fmt.Sprintf("%d CLI(s) have synced secrets under a name they do not read: %s", len(mismatches), strings.Join(mismatches, ", ")),
		Remediation: "run `agentcookie discover` for the detail, then `agentcookie secret alias <cli> <declared-env-var> <synced-key>`",
	}
}

// checkBinaryInstall catches the footgun where multiple agentcookie binaries
// exist on the machine (e.g. ~/go/bin/agentcookie stale on PATH while the
// daemon runs ~/bin/agentcookie). When they differ, the binary a user invokes
// for status/doctor may not be the one the daemon runs, producing misleading
// output. WARN, never FAIL.
func checkBinaryInstall() Check {
	home, _ := os.UserHomeDir()
	candidates := []string{
		filepath.Join(home, "go", "bin", "agentcookie"),
		filepath.Join(home, "bin", "agentcookie"),
		"/usr/local/bin/agentcookie",
		"/opt/homebrew/bin/agentcookie",
	}
	if p, err := exec.LookPath("agentcookie"); err == nil {
		candidates = append(candidates, p)
	}
	if self, err := os.Executable(); err == nil {
		candidates = append(candidates, self)
	}
	return binaryInstallCheckFrom(candidates)
}

// binaryInstallCheckFrom is the testable core of checkBinaryInstall over an
// explicit candidate list (so tests don't depend on the host's real PATH).
func binaryInstallCheckFrom(candidates []string) Check {
	type binInfo struct {
		path string
		size int64
		mod  time.Time
	}
	seen := map[string]binInfo{}
	for _, c := range candidates {
		rp, err := filepath.EvalSymlinks(c)
		if err != nil {
			continue
		}
		if _, ok := seen[rp]; ok {
			continue
		}
		fi, err := os.Stat(rp)
		if err != nil || fi.IsDir() {
			continue
		}
		seen[rp] = binInfo{path: rp, size: fi.Size(), mod: fi.ModTime()}
	}

	if len(seen) <= 1 {
		return Check{Name: "Binary install", Severity: SeverityOK, Detail: "single agentcookie binary on this machine"}
	}

	infos := make([]binInfo, 0, len(seen))
	for _, v := range seen {
		infos = append(infos, v)
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].path < infos[j].path })
	differ := false
	for i := 1; i < len(infos); i++ {
		if infos[i].size != infos[0].size || !infos[i].mod.Equal(infos[0].mod) {
			differ = true
			break
		}
	}
	paths := make([]string, 0, len(infos))
	for _, in := range infos {
		paths = append(paths, in.path)
	}
	if !differ {
		return Check{Name: "Binary install", Severity: SeverityOK, Detail: fmt.Sprintf("%d identical agentcookie binaries (%s)", len(infos), strings.Join(paths, ", "))}
	}
	return Check{
		Name:        "Binary install",
		Severity:    SeverityWarn,
		Detail:      fmt.Sprintf("%d agentcookie binaries differ; the one on PATH may not be the one your daemon runs: %s", len(infos), strings.Join(paths, ", ")),
		Remediation: "reinstall so every location is the same build, or delete the stale copy, so status/doctor reflect the running daemon",
	}
}

// --- individual checks ---

// checkBinarySignatureWith parses the output of `codesign -d -r-`. The
// production caller passes probeBinarySignature; tests inject a string.
// Severity is OK when the designated requirement names Team ID
// NM8VT393AR; WARN otherwise (ad-hoc local build or no codesign). The
// check never FAILs because a friend running a freshly-built dev binary
// should not be blocked.
func checkBinarySignatureWith(probe func() (string, error)) Check {
	out, err := probe()
	if err != nil {
		return Check{
			Name:        "Binary signature",
			Severity:    SeverityWarn,
			Detail:      "codesign unavailable (" + err.Error() + ")",
			Remediation: "install Xcode command-line tools if you want signature verification",
		}
	}
	if strings.Contains(out, "NM8VT393AR") {
		return Check{
			Name:     "Binary signature",
			Severity: SeverityOK,
			Detail:   "Developer ID Application (NM8VT393AR)",
		}
	}
	return Check{
		Name:        "Binary signature",
		Severity:    SeverityWarn,
		Detail:      "ad-hoc signed (local build); not a release binary",
		Remediation: "fine for development; install the notarized release binary for production",
	}
}

// probeBinarySignature shells out to `codesign -d -r- <self>` and
// returns the combined output. macOS prints the designated requirement
// (and Team ID for signed binaries) on stderr; we capture both so the
// caller can string-match the Team ID.
func probeBinarySignature() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	cmd := exec.Command("/usr/bin/codesign", "-d", "-r-", exe)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// checkTailscaleWith calls the injected RequireTailnetIP-equivalent
// and turns the structured error into a remediation pointing at
// `tailscale up`. The tsclient package already produces a detailed
// inner error message; doctor surfaces the first line and pins the
// remediation to the most common fix.
func checkTailscaleWith(probe func() (string, error)) Check {
	ip, err := probe()
	if err != nil {
		return Check{
			Name:        "Tailscale",
			Severity:    SeverityFail,
			Detail:      err.Error(),
			Remediation: "run `tailscale up` and re-run; see docs/quickstart.md",
		}
	}
	return Check{
		Name:     "Tailscale",
		Severity: SeverityOK,
		Detail:   ip + " reachable on local tailnet interface",
	}
}

// checkConfig is the test-facing wrapper that drops the parsed configs
// (the integration test only cares about Severity).
func checkConfig(configDir string) Check {
	c, _, _ := checkConfigLoaded(configDir)
	return c
}

// checkConfigLoaded is the real check. Returns the Check plus the
// parsed configs so downstream checks (keystore, listener, state) can
// branch on role without re-loading.
//
// Exactly one of source.yaml or sink.yaml is required; both may be
// present on a single-machine local-dev install. Neither = FAIL.
// Parse errors are surfaced as FAIL with the file path embedded so
// the user knows which file to look at.
func checkConfigLoaded(configDir string) (Check, *config.SourceConfig, *config.SinkConfig) {
	srcPath := filepath.Join(configDir, "source.yaml")
	sinkPath := filepath.Join(configDir, "sink.yaml")

	srcExists := fileExists(srcPath)
	sinkExists := fileExists(sinkPath)

	if !srcExists && !sinkExists {
		return Check{
			Name:        "Config",
			Severity:    SeverityFail,
			Detail:      "neither source.yaml nor sink.yaml present in " + configDir,
			Remediation: "run `agentcookie wizard install --as source` (laptop) or `--as sink` (Mac mini)",
		}, nil, nil
	}

	var (
		srcCfg  *config.SourceConfig
		sinkCfg *config.SinkConfig
		parts   []string
		errs    []string
	)
	if srcExists {
		s, err := config.LoadSource(configDir)
		if err != nil {
			errs = append(errs, "source.yaml: "+err.Error())
		} else {
			srcCfg = s
			parts = append(parts, "source.yaml")
		}
	}
	if sinkExists {
		s, err := config.LoadSink(configDir)
		if err != nil {
			errs = append(errs, "sink.yaml: "+err.Error())
		} else {
			sinkCfg = s
			parts = append(parts, "sink.yaml")
		}
	}

	if len(errs) > 0 {
		return Check{
			Name:        "Config",
			Severity:    SeverityFail,
			Detail:      strings.Join(errs, "; "),
			Remediation: "fix the YAML or re-run `agentcookie wizard install`",
		}, srcCfg, sinkCfg
	}

	return Check{
		Name:     "Config",
		Severity: SeverityOK,
		Detail:   strings.Join(parts, " + ") + " present, parses OK",
	}, srcCfg, sinkCfg
}

// checkKeystore validates that every configured peer hostname has a
// key file at ~/.config/agentcookie/keys/<peer>.json with mode 0600.
// Missing or wrong-mode = FAIL; no peers configured = SKIPPED (config
// check already FAILed in that case, so doctor doesn't double-bill).
func checkKeystore(configDir string, peers []string) Check {
	if len(peers) == 0 {
		return Check{
			Name:     "Keystore",
			Severity: SeveritySkipped,
			Detail:   "no peer hostname configured",
		}
	}
	var problems []string
	for _, peer := range peers {
		path, err := keystore.Path(configDir, peer)
		if err != nil {
			problems = append(problems, peer+": "+err.Error())
			continue
		}
		fi, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				problems = append(problems, "missing key for peer "+peer)
			} else {
				problems = append(problems, peer+": "+err.Error())
			}
			continue
		}
		// On macOS the underlying mode bits include the type; mask to
		// permission bits before comparing. 0600 is the only acceptable
		// mode -- the keystore.Save path writes it explicitly.
		if mode := fi.Mode().Perm(); mode != 0o600 {
			problems = append(problems, fmt.Sprintf("%s has mode %#o (want 0600)", peer, mode))
		}
	}
	if len(problems) > 0 {
		return Check{
			Name:        "Keystore",
			Severity:    SeverityFail,
			Detail:      strings.Join(problems, "; "),
			Remediation: "run `agentcookie pair` to (re-)derive the peer key",
		}
	}
	return Check{
		Name:     "Keystore",
		Severity: SeverityOK,
		Detail:   fmt.Sprintf("peer key for %s present (mode 0600)", strings.Join(peers, ", ")),
	}
}

// checkSinkListener tries to bind the configured address ourselves.
// If the bind succeeds, the configured sink port is NOT in use --
// which means the LaunchAgent is not running, regardless of what
// any state file says. The competing-bind probe is the operational
// truth.
//
// Note: this only proves *something* is listening on that port; it
// does not prove it's the agentcookie sink. The combination of (port
// bound) + (recent sink-state.json) is what tells us the sink is the
// listener; check #6 covers the second half.
func checkSinkListener(addr string) Check {
	if addr == "" {
		return Check{
			Name:        "Sink listener",
			Severity:    SeverityFail,
			Detail:      "no listen address configured",
			Remediation: "re-run `agentcookie wizard install --as sink`",
		}
	}
	ln, err := net.Listen("tcp", addr)
	if err == nil {
		// We were able to bind -- nothing else is listening.
		_ = ln.Close()
		return Check{
			Name:        "Sink listener",
			Severity:    SeverityFail,
			Detail:      "nothing bound on " + addr,
			Remediation: "launchctl bootstrap gui/$UID ~/Library/LaunchAgents/dev.agentcookie.sink.plist",
		}
	}
	return Check{
		Name:     "Sink listener",
		Severity: SeverityOK,
		Detail:   "bound on " + addr,
	}
}

// checkSinkStateFrom branches on the age of LastWrite. Missing state
// file (nil + nil err) is FAIL because a configured sink with no state
// has never run. Read error is also FAIL. Age >24h is WARN so an idle
// weekend doesn't break exit-zero on a sink that's otherwise healthy.
func checkSinkStateFrom(st *state.SinkState, err error) Check {
	if err != nil {
		return Check{
			Name:        "Sink state",
			Severity:    SeverityFail,
			Detail:      "read sink-state.json: " + err.Error(),
			Remediation: "check ~/.agentcookie/ permissions",
		}
	}
	if st == nil {
		return Check{
			Name:        "Sink state",
			Severity:    SeverityFail,
			Detail:      "sink-state.json missing; sink has never accepted a write",
			Remediation: "run `agentcookie source --once` on the MacBook to push the first batch",
		}
	}
	age := time.Since(st.LastWrite).Round(time.Second)
	if st.LastWrite.IsZero() || age > 24*time.Hour {
		return Check{
			Name:        "Sink state",
			Severity:    SeverityWarn,
			Detail:      fmt.Sprintf("last write %s ago (>24h)", age),
			Remediation: "run `agentcookie source --once` on the source side",
		}
	}
	mode := st.LastWriteMode
	if mode == "" {
		mode = "unknown"
	}
	return Check{
		Name:     "Sink state",
		Severity: SeverityOK,
		Detail:   fmt.Sprintf("last write %s ago, mode=%s, %d rejected", age, mode, st.TotalRejects),
	}
}

// checkSourceStateFrom mirrors checkSinkStateFrom for the source role.
// >24h since last push OR total_failures > 0 = WARN; missing file = FAIL.
func checkSourceStateFrom(st *state.SourceState, err error) Check {
	if err != nil {
		return Check{
			Name:        "Source state",
			Severity:    SeverityFail,
			Detail:      "read source-state.json: " + err.Error(),
			Remediation: "check ~/.agentcookie/ permissions",
		}
	}
	if st == nil {
		return Check{
			Name:        "Source state",
			Severity:    SeverityFail,
			Detail:      "source-state.json missing; the source daemon has never pushed",
			Remediation: "run `agentcookie source --once` to do the first push",
		}
	}
	age := time.Since(st.LastPush).Round(time.Second)
	if st.LastPush.IsZero() || age > 24*time.Hour {
		return Check{
			Name:        "Source state",
			Severity:    SeverityWarn,
			Detail:      fmt.Sprintf("last push %s ago (>24h)", age),
			Remediation: "run `agentcookie source --once` to manually trigger a push",
		}
	}
	if st.TotalFailures > 0 {
		return Check{
			Name:        "Source state",
			Severity:    SeverityWarn,
			Detail:      fmt.Sprintf("last push %s ago, %d total failures", age, st.TotalFailures),
			Remediation: "inspect `agentcookie status` for the most recent error",
		}
	}
	return Check{
		Name:     "Source state",
		Severity: SeverityOK,
		Detail:   fmt.Sprintf("last push %s ago, 0 failures", age),
	}
}

// checkDBSCFrom is informational: it surfaces how many cookies the last push
// flagged as Device Bound Session Credentials (DBSC) suspects -- cookies bound
// to this Mac that likely will not work on the sink. Zero suspects (or no
// state yet) is OK; any suspects are a WARN so the user understands why those
// sessions may not carry over.
func checkDBSCFrom(st *state.SourceState) Check {
	if st == nil || (st.LastDBSCWarned == 0 && st.LastDBSCSkipped == 0) {
		return Check{
			Name:     "DBSC",
			Severity: SeverityOK,
			Detail:   "no device-bound (DBSC) cookies flagged in the last push",
		}
	}
	detail := fmt.Sprintf("last push flagged %d shipped-with-warning, %d skipped DBSC-suspect cookie(s)", st.LastDBSCWarned, st.LastDBSCSkipped)
	if len(st.LastDBSCSample) > 0 {
		detail += ": " + st.LastDBSCSample[0]
	}
	return Check{
		Name:        "DBSC",
		Severity:    SeverityWarn,
		Detail:      detail,
		Remediation: "these cookies are device-bound and may not work on the sink; for Google sessions, sign the sink's Chrome into the same account (see README: DBSC)",
	}
}

// checkSealingWith is informational: in v0.12 closed beta the master
// key Keychain item is off by default. Doctor reports the state so
// agents downstream can branch on it, but never WARN/FAIL on either
// branch -- the user-facing default is "disabled" for this release.
func checkSealingWith(masterKeyExists func() bool) Check {
	if masterKeyExists() {
		return Check{
			Name:     "Sealing",
			Severity: SeverityOK,
			Detail:   "enabled (agentcookie-master Keychain item present)",
		}
	}
	return Check{
		Name:     "Sealing",
		Severity: SeverityOK,
		Detail:   "disabled (default in v0.12 closed beta)",
	}
}

// checkAdapterCoverage (v0.12.0-beta.3) reports which cookie host_keys
// in the sidecar have NO matching adapter. The sidecar itself always
// covers sidecar-aware PP CLIs (anything linking pkg/sidecar), so a
// gap here is only meaningful for kooky-only readers. WARN, not FAIL.
//
// Implementation: read the sidecar's unique host_keys, match each
// against the host patterns of every registered adapter. Hosts with
// zero adapter matches are reported, capped at top 3 by frequency so
// the output stays short.
func checkAdapterCoverage(_ *config.SinkConfig) Check {
	sidecarPath := chromepaths.SidecarCookiesDB()
	uniqueHosts, err := chrome.SidecarUniqueHostKeys(sidecarPath)
	if err != nil {
		// No sidecar yet means no syncs have hit this sink. Skip; the
		// sink-state check already reports "no writes yet" as FAIL.
		return Check{
			Name:     "Adapter coverage",
			Severity: SeveritySkipped,
			Detail:   "no sidecar yet (no syncs received); rerun after first sync",
		}
	}
	if len(uniqueHosts) == 0 {
		return Check{
			Name:     "Adapter coverage",
			Severity: SeverityOK,
			Detail:   "sidecar empty; nothing to cover",
		}
	}
	adapters := sinkpush.All()
	var uncovered []string
	for _, h := range uniqueHosts {
		if !hostMatchesAnyAdapter(h, adapters) {
			uncovered = append(uncovered, h)
		}
	}
	if len(uncovered) == 0 {
		return Check{
			Name:     "Adapter coverage",
			Severity: SeverityOK,
			Detail:   fmt.Sprintf("all %d host_keys in sidecar covered by an adapter", len(uniqueHosts)),
		}
	}
	preview := uncovered
	if len(preview) > 3 {
		preview = preview[:3]
	}
	return Check{
		Name:        "Adapter coverage",
		Severity:    SeverityWarn,
		Detail:      fmt.Sprintf("%d of %d host_keys in sidecar have no adapter: %s", len(uncovered), len(uniqueHosts), strings.Join(preview, ", ")),
		Remediation: "kooky-only PP CLIs reading these hosts will fall back to Chrome's encrypted store; sidecar-aware PP CLIs (linking pkg/sidecar) are unaffected. See docs/quickstart-beta.md for the sidecar env-var integration path.",
	}
}

// hostMatchesAnyAdapter returns true if hostKey matches at least one
// of any adapter's CookieHostPatterns. Patterns are SQLite LIKE syntax
// with `%` wildcards; the doctor approximates them as case-sensitive
// substring matches with `%` stripped (good enough for warn-level
// visibility; exact match semantics live in the writer path).
func hostMatchesAnyAdapter(hostKey string, adapters []sinkpush.Adapter) bool {
	for _, a := range adapters {
		for _, p := range a.CookieHostPatterns() {
			needle := strings.TrimSuffix(strings.TrimPrefix(p, "%"), "%")
			if needle != "" && strings.Contains(hostKey, needle) {
				return true
			}
		}
	}
	return false
}

// checkCDPInjector (v0.12.0-beta.3) verifies the CDP-injection mode is
// usable: cdp.profile_dir exists and is writable, AND Chrome.app is
// installed on this Mac. WARN when configured but unusable; SKIPPED
// when cdp.enabled is false.
func checkCDPInjector(sinkCfg *config.SinkConfig) Check {
	if !sinkCfg.CDP.Enabled {
		return Check{
			Name:     "CDP injector",
			Severity: SeveritySkipped,
			Detail:   "cdp.enabled is false (legacy mode or --no-cdp)",
		}
	}
	profileDir := sinkCfg.CDP.ProfileDir
	if profileDir == "" {
		profileDir = "~/.agentcookie/chrome-profile"
	}
	expanded := profileDir
	if len(expanded) > 0 && expanded[0] == '~' {
		if home, herr := os.UserHomeDir(); herr == nil {
			expanded = filepath.Join(home, expanded[1:])
		}
	}
	if _, err := os.Stat(expanded); err != nil {
		return Check{
			Name:        "CDP injector",
			Severity:    SeverityWarn,
			Detail:      "profile_dir does not exist: " + profileDir,
			Remediation: "re-run `agentcookie wizard install --as sink` to create the directory, or `mkdir -p " + profileDir + "`",
		}
	}
	for _, p := range []string{"/Applications/Google Chrome.app", filepath.Join(os.Getenv("HOME"), "Applications", "Google Chrome.app")} {
		if _, err := os.Stat(p); err == nil {
			return Check{
				Name:     "CDP injector",
				Severity: SeverityOK,
				Detail:   "profile_dir=" + profileDir + " is the synced/logged-in profile (your default Chrome profile is intentionally not written), Chrome=" + p,
			}
		}
	}
	return Check{
		Name:        "CDP injector",
		Severity:    SeverityWarn,
		Detail:      "Chrome.app not found in /Applications or ~/Applications; CDP injection will fail at sync time",
		Remediation: "install Google Chrome from https://www.google.com/chrome/, or pass --no-cdp at wizard install to disable CDP injection",
	}
}

// checkCookieDelivery (v0.13 universal cookie delivery) reports whether
// ANY unmodified cookie CLI works on this sink, or only agentcookie-aware
// tools. Universal delivery requires two facts to both hold:
//
//	a. The real Default Chrome profile is written (skip_chrome_sqlite=false),
//	   so a cookie CLI reading Chrome's default profile sees synced sessions.
//	b. The Chrome Safe Storage key is readable by any local process (a
//	   non-zero key length with no error from KeybaseKeychainProbe), so an
//	   unmodified CLI can decrypt those cookies.
//
// sinkCfg.Delivery is the recorded intent ("universal" | "degraded"); it
// phrases the message, but the live probe is the source of truth. The
// exported check wires the real KeybaseKeychainProbe; checkCookieDeliveryWith
// is the testable core over an injected probe.
func checkCookieDelivery(sinkCfg *config.SinkConfig) Check {
	return checkCookieDeliveryWith(sinkCfg,
		func() (int, error) { return chrome.KeybaseKeychainProbe(3 * time.Second) },
		chrome.CountSafeStorageItems,
	)
}

// oneePasswordGrantRemediation is the canonical one-password grant instruction.
// It is the SSH-safe inline partition path (one login-password entry on the
// terminal, no GUI SecurityAgent click) — NOT the obsolete `--any-app` recreate,
// which the live macOS 15.x verification (2026-05-31) proved unnecessary for the
// signed daemon: the partition with teamid:<team> grants the daemon read and the
// apple-tool: entry grants security-CLI tools (yt-dlp, gallery-dl).
const onePasswordGrantRemediation = "grant access over SSH with one login-password entry: `agentcookie wizard set-keychain-access` (non-interactive: AGENTCOOKIE_LOGIN_PASSWORD=… agentcookie wizard set-keychain-access)"

// checkCookieDeliveryWith is the testable core of checkCookieDelivery over an
// injected keychain probe and item counter (so tests don't depend on the host
// Keychain). A probe returning n>0 with no error means the Chrome Safe Storage
// key is readable. countItems reports how many Chrome Safe Storage keychain
// items exist; >1 is the install-time duplicate-item race.
func checkCookieDeliveryWith(sinkCfg *config.SinkConfig, probe func() (int, error), countItems func() (int, error)) Check {
	if sinkCfg == nil {
		return Check{
			Name:     "Cookie delivery",
			Severity: SeveritySkipped,
			Detail:   "source-only install",
		}
	}

	realProfile := !sinkCfg.SkipChromeSQLite

	// Degraded by configuration: the sink intentionally skips Chrome's real
	// SQLite/Default profile. Only agentcookie-aware tools (sidecar/adapter
	// readers) see synced cookies. INFO, not a failure -- this is a valid
	// supported mode (headless / SSH-only Mac minis).
	if !realProfile {
		return Check{
			Name:        "Cookie delivery",
			Severity:    SeverityInfo,
			Detail:      "degraded: writes a separate profile; only agentcookie-aware tools work",
			Remediation: "set delivery universal (skip_chrome_sqlite=false), then " + onePasswordGrantRemediation,
		}
	}

	keyLen, err := probe()
	keyReadable := err == nil && keyLen > 0

	// Duplicate-item race takes priority over a clean OK/partial verdict: even
	// when the key reads now, more than one Chrome Safe Storage item means a
	// later reader can hit the ungranted duplicate. It is also detectable over
	// a locked SSH session (dump-keychain reads metadata without unlocking), so
	// it is the most actionable signal we can surface there.
	if n, cerr := countItems(); cerr == nil && n > 1 {
		return Check{
			Name:        "Cookie delivery",
			Severity:    SeverityWarn,
			Detail:      fmt.Sprintf("race: %d Chrome Safe Storage keychain items exist; the install-time Chrome-relaunch race left duplicates, so a cookie reader may hit a different item than the one granted access", n),
			Remediation: "converge to one item and re-grant: `agentcookie wizard set-keychain-access` (quiesces Chrome, collapses duplicates value-preserved, re-sets the partition)",
		}
	}

	if keyReadable {
		detail := "universal: real Default profile written and Chrome Safe Storage readable; any unmodified cookie CLI works here"
		if sinkCfg.Delivery == "universal" {
			detail = "universal (delivery=universal): real Default profile written and Chrome Safe Storage readable; any unmodified cookie CLI works here"
		}
		return Check{
			Name:     "Cookie delivery",
			Severity: SeverityOK,
			Detail:   detail,
		}
	}

	// Real profile written but the probe could not read the key. Distinguish a
	// LOCKED login keychain (expected over SSH; the GUI-session daemon and a
	// logged-in desktop read it fine) from a genuinely ungranted key. The
	// locked case is a false negative, not a failure, so it is INFO with no
	// destructive remediation.
	if chrome.IsKeychainLocked(err) {
		return Check{
			Name:        "Cookie delivery",
			Severity:    SeverityInfo,
			Detail:      "universal config; the Chrome Safe Storage key can't be verified from this session because the login keychain is locked (expected over SSH). The sink daemon reads it in its unlocked GUI session, and a logged-in Mac is unlocked too",
			Remediation: "if an unmodified cookie CLI on this box can't decrypt, unlock first (`security unlock-keychain`) or run it from the GUI session; the grant itself is one-password via `agentcookie wizard set-keychain-access`",
		}
	}

	// Genuinely not readable (not locked, single item): the one-password grant
	// has not run on this box. An unmodified CLI sees the cookies but can't
	// decrypt them.
	return Check{
		Name:        "Cookie delivery",
		Severity:    SeverityWarn,
		Detail:      "partial: real Default profile written but the Chrome Safe Storage key has not been granted to cookie readers; unmodified cookie CLIs can't decrypt synced cookies",
		Remediation: onePasswordGrantRemediation,
	}
}

// --- output ---

// printHuman renders the report to w in the human-readable shape
// documented in U5's plan. Severity labels are uppercase to read at a
// glance; remediation hangs below each non-OK line.
func printHuman(w *os.File, r DoctorReport) {
	fmt.Fprintf(w, "agentcookie doctor %s\n", r.Version)
	var fails, warns int
	for _, c := range r.Checks {
		fmt.Fprintf(w, "  %s %s: %s\n", severityTag(c.Severity), c.Name, c.Detail)
		if c.Remediation != "" && (c.Severity == SeverityFail || c.Severity == SeverityWarn) {
			fmt.Fprintf(w, "         Remediation: %s\n", c.Remediation)
		}
		switch c.Severity {
		case SeverityFail:
			fails++
		case SeverityWarn:
			warns++
		}
	}
	switch {
	case fails > 0:
		fmt.Fprintf(w, "%d FAIL, %d WARN -- see remediations above\n", fails, warns)
	case warns > 0:
		fmt.Fprintf(w, "%d WARN (informational); install otherwise green\n", warns)
	default:
		fmt.Fprintln(w, "all green")
	}
}

// severityTag returns the bracketed label used in human output.
// Centralized so the tag set is one edit away from changing.
func severityTag(s Severity) string {
	switch s {
	case SeverityOK:
		return "[OK]  "
	case SeverityWarn:
		return "[WARN]"
	case SeverityFail:
		return "[FAIL]"
	case SeverityInfo:
		return "[INFO]"
	case SeveritySkipped:
		return "[--]  "
	default:
		return "[??]  "
	}
}

// (fileExists lives in wizard.go and is reused here.)

// checkSecretsBus (v0.13) reports the state of the agentcookie secrets
// bus: how many CLIs are registered, how many keys total, whether
// sealing is in effect (sealed twins present), and how recently any
// file in the tree was touched.
//
// Reports SKIPPED when the secrets root doesn't exist (most installs
// today; the bus is opt-in). OK when populated and reasonably fresh.
// WARN when sealed twins exist but the master key is missing (the
// inverse warning of "sealing requested but master key absent" we
// surface at write time, so a reader hitting a sealed file later knows
// what they're looking at).
func checkSecretsBus() Check {
	home, _ := os.UserHomeDir()
	root := filepath.Join(home, ".agentcookie", "secrets")
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return Check{
				Name:     "Secrets bus",
				Severity: SeveritySkipped,
				Detail:   "no secrets bus configured (~/.agentcookie/secrets/ is empty or absent)",
			}
		}
		return Check{
			Name:        "Secrets bus",
			Severity:    SeverityFail,
			Detail:      "read secrets root: " + err.Error(),
			Remediation: "check ~/.agentcookie/secrets/ permissions",
		}
	}

	var (
		cliCount   int
		keyCount   int
		sealedCLIs int
		newestMod  time.Time
	)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		cliDir := filepath.Join(root, e.Name())
		envPath := filepath.Join(cliDir, "secrets.env")
		sealedPath := filepath.Join(cliDir, "secrets.env.sealed")
		envInfo, envErr := os.Stat(envPath)
		sealedInfo, sealedErr := os.Stat(sealedPath)
		if envErr != nil && sealedErr != nil {
			continue
		}
		cliCount++
		if sealedErr == nil {
			sealedCLIs++
			if sealedInfo.ModTime().After(newestMod) {
				newestMod = sealedInfo.ModTime()
			}
		}
		if envErr == nil {
			if data, readErr := os.ReadFile(envPath); readErr == nil {
				keyCount += countEnvKeys(data)
			}
			if envInfo.ModTime().After(newestMod) {
				newestMod = envInfo.ModTime()
			}
		}
	}
	if cliCount == 0 {
		return Check{
			Name:     "Secrets bus",
			Severity: SeveritySkipped,
			Detail:   "secrets root exists but contains no CLIs yet",
		}
	}

	mode := "plaintext"
	if sealedCLIs == cliCount {
		mode = "sealed"
	} else if sealedCLIs > 0 {
		mode = fmt.Sprintf("mixed (%d sealed / %d plaintext)", sealedCLIs, cliCount-sealedCLIs)
	}
	freshness := "never"
	if !newestMod.IsZero() {
		freshness = time.Since(newestMod).Round(time.Second).String() + " ago"
	}
	return Check{
		Name:     "Secrets bus",
		Severity: SeverityOK,
		Detail:   fmt.Sprintf("%d cli(s), %d key(s), mode=%s, newest %s", cliCount, keyCount, mode, freshness),
	}
}

// countEnvKeys counts non-comment non-blank lines containing '='.
// Cheap proxy for the key count without parsing every value.
func countEnvKeys(data []byte) int {
	n := 0
	for _, line := range bytesSplitLines(data) {
		trim := bytesTrimSpace(line)
		if len(trim) == 0 || trim[0] == '#' {
			continue
		}
		for _, b := range trim {
			if b == '=' {
				n++
				break
			}
		}
	}
	return n
}

func bytesSplitLines(data []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			out = append(out, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		out = append(out, data[start:])
	}
	return out
}

func bytesTrimSpace(b []byte) []byte {
	i := 0
	for i < len(b) && (b[i] == ' ' || b[i] == '\t') {
		i++
	}
	j := len(b)
	for j > i && (b[j-1] == ' ' || b[j-1] == '\t' || b[j-1] == '\r') {
		j--
	}
	return b[i:j]
}
