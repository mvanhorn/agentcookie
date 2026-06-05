package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/mvanhorn/agentcookie/internal/agentattach"
	"github.com/mvanhorn/agentcookie/internal/agentbrowser"
)

var (
	attachTarget   string
	attachPort     int
	attachPrint    bool
	attachWire     bool
	attachCheck    bool
	attachFallback bool
)

var attachCmd = &cobra.Command{
	Use:   "attach",
	Short: "Attach a Chromium agent browser (browser-use, agent-browser) to your real Chrome over CDP",
	Long: `attach points a Chromium agent browser at your real Chrome over the
Chrome DevTools Protocol, so the agent browser shares your live cookies,
localStorage, and device-bound sessions instead of running an empty
profile. This is the fix for "are you sure you're logged in?" -- the agent
browser becomes the browser you are already logged into, rather than a copy.

It probes a loopback Chrome debug endpoint (default 127.0.0.1:9222),
detects the Chrome version policy tier, and wires the agent browser to
attach. On Chrome 144+ the real default profile is attachable once you
enable it via chrome://inspect#remote-debugging; attach prints those steps
when the endpoint is not yet reachable.

  agentcookie attach                 wire every installed agent browser (default)
  agentcookie attach --print         show the endpoint + launch snippets, write nothing
  agentcookie attach --check         report reachability and policy tier
  agentcookie attach --target browser-use --wire

cmux is WebKit and cannot CDP-attach; it uses the separate ` + "`agentcookie cmux-sync`" + ` loop.`,
	RunE: runAttach,
}

func init() {
	attachCmd.Flags().StringVar(&attachTarget, "target", "all", "agent browser to wire: browser-use | agent-browser | all")
	attachCmd.Flags().IntVar(&attachPort, "port", agentattach.DefaultDebugPort, "loopback Chrome remote-debugging port to probe")
	attachCmd.Flags().BoolVar(&attachPrint, "print", false, "print the CDP endpoint and launch snippets without writing anything")
	attachCmd.Flags().BoolVar(&attachWire, "wire", false, "write launcher wrappers so the agent browser always attaches (default action)")
	attachCmd.Flags().BoolVar(&attachCheck, "check", false, "report attach reachability and policy tier")
	attachCmd.Flags().BoolVar(&attachFallback, "fallback", false, "use a synced debug Chrome profile instead of attaching the real profile (for older Chrome or an isolated session)")
}

func runAttach(cmd *cobra.Command, args []string) error {
	wirers, err := selectWirers(attachTarget)
	if err != nil {
		return err
	}

	// --fallback is its own path: it does not probe-then-wire the real
	// profile, it stands up a synced debug profile and wires to that.
	if attachFallback {
		return fallbackAttach(cmd.Context(), os.Stdout, wirers, attachPort)
	}

	action, err := resolveAttachAction(attachPrint, attachWire, attachCheck)
	if err != nil {
		return err
	}

	d := agentattach.Discover(cmd.Context(), attachPort)
	target := agentbrowser.AttachTarget{Port: attachPort, WSEndpoint: d.WSEndpoint}

	switch action {
	case "print":
		return printAttach(cmd.Context(), os.Stdout, d, wirers, target, common.JSON)
	case "check":
		return checkAttach(os.Stdout, d, wirers, common.JSON)
	default:
		return wireAttach(os.Stdout, d, wirers, target, common.JSON)
	}
}

// resolveAttachAction picks the single action; wire is the default.
func resolveAttachAction(print, wire, check bool) (string, error) {
	set := 0
	for _, b := range []bool{print, wire, check} {
		if b {
			set++
		}
	}
	if set > 1 {
		return "", fmt.Errorf("choose at most one of --print, --wire, --check")
	}
	switch {
	case print:
		return "print", nil
	case check:
		return "check", nil
	default:
		return "wire", nil
	}
}

// selectWirers resolves the --target value to the set of wirers to act on.
func selectWirers(target string) ([]agentbrowser.Wirer, error) {
	switch target {
	case "", "all":
		return agentbrowser.All(), nil
	default:
		w, ok := agentbrowser.Lookup(target)
		if !ok {
			return nil, fmt.Errorf("unknown --target %q (want: browser-use, agent-browser, or all)", target)
		}
		return []agentbrowser.Wirer{w}, nil
	}
}

func printAttach(_ context.Context, out io.Writer, d agentattach.Discovery, wirers []agentbrowser.Wirer, target agentbrowser.AttachTarget, asJSON bool) error {
	if asJSON {
		type snip struct {
			Name      string `json:"name"`
			Installed bool   `json:"installed"`
			Snippet   string `json:"snippet"`
		}
		payload := struct {
			Reachable   bool   `json:"reachable"`
			Version     int    `json:"chrome_version"`
			Tier        string `json:"policy_tier"`
			WSEndpoint  string `json:"ws_endpoint,omitempty"`
			Remediation string `json:"remediation,omitempty"`
			Targets     []snip `json:"targets"`
		}{Reachable: d.Reachable, Version: d.Version, Tier: d.Tier.String(), WSEndpoint: d.WSEndpoint, Remediation: d.Remediation}
		for _, w := range wirers {
			payload.Targets = append(payload.Targets, snip{w.Name(), w.IsInstalled(), w.LaunchSnippet(target)})
		}
		return json.NewEncoder(out).Encode(payload)
	}

	fmt.Fprintln(out, attachStatusLine(d))
	for _, w := range wirers {
		if !w.IsInstalled() {
			fmt.Fprintf(out, "  %-14s not installed (skipped)\n", w.Name())
			continue
		}
		fmt.Fprintf(out, "  %-14s %s\n", w.Name(), w.LaunchSnippet(target))
	}
	if d.Remediation != "" {
		fmt.Fprintf(out, "\n%s\n", d.Remediation)
	}
	return nil
}

func checkAttach(out io.Writer, d agentattach.Discovery, wirers []agentbrowser.Wirer, asJSON bool) error {
	if asJSON {
		type tgt struct {
			Name      string `json:"name"`
			Installed bool   `json:"installed"`
		}
		payload := struct {
			Reachable   bool   `json:"reachable"`
			Version     int    `json:"chrome_version"`
			Tier        string `json:"policy_tier"`
			Remediation string `json:"remediation,omitempty"`
			Targets     []tgt  `json:"targets"`
		}{Reachable: d.Reachable, Version: d.Version, Tier: d.Tier.String(), Remediation: d.Remediation}
		for _, w := range wirers {
			payload.Targets = append(payload.Targets, tgt{w.Name(), w.IsInstalled()})
		}
		if err := json.NewEncoder(out).Encode(payload); err != nil {
			return err
		}
		if !d.Reachable {
			return errNotReachable
		}
		return nil
	}

	fmt.Fprintln(out, attachStatusLine(d))
	for _, w := range wirers {
		state := "installed"
		if !w.IsInstalled() {
			state = "not installed"
		}
		fmt.Fprintf(out, "  %-14s %s\n", w.Name(), state)
	}
	if !d.Reachable {
		fmt.Fprintf(out, "\n%s\n", d.Remediation)
		return errNotReachable
	}
	return nil
}

func wireAttach(out io.Writer, d agentattach.Discovery, wirers []agentbrowser.Wirer, target agentbrowser.AttachTarget, asJSON bool) error {
	// Wiring bakes the loopback port into a launcher. That only produces a
	// working attach when the real profile is (or can be made) attachable:
	// reachable now, or Chrome 144+ where the user can enable it via
	// chrome://inspect. On older Chrome the real profile can never be
	// attached on this port -- point the user at the fallback instead.
	if !d.Reachable && d.Tier != agentattach.TierAutoConnect {
		fmt.Fprintln(out, attachStatusLine(d))
		fmt.Fprintf(out, "\n%s\n", d.Remediation)
		return fmt.Errorf("not wiring: the real profile cannot be attached on port %d for this Chrome", target.Port)
	}

	installed := 0
	var results []wireOutcome
	for _, w := range wirers {
		if !w.IsInstalled() {
			results = append(results, wireOutcome{Name: w.Name(), Skipped: true, Reason: "not installed"})
			continue
		}
		res, err := w.Wire(target)
		if err != nil {
			results = append(results, wireOutcome{Name: w.Name(), Error: err.Error()})
			continue
		}
		installed++
		results = append(results, wireOutcome{Name: w.Name(), Launcher: res.LauncherPath, Note: res.Note})
	}

	if asJSON {
		payload := struct {
			Reachable   bool          `json:"reachable"`
			Tier        string        `json:"policy_tier"`
			Remediation string        `json:"remediation,omitempty"`
			Wired       []wireOutcome `json:"wired"`
		}{Reachable: d.Reachable, Tier: d.Tier.String(), Remediation: nonEmptyIfUnreachable(d), Wired: results}
		if err := json.NewEncoder(out).Encode(payload); err != nil {
			return err
		}
	} else {
		fmt.Fprintln(out, attachStatusLine(d))
		for _, r := range results {
			switch {
			case r.Skipped:
				fmt.Fprintf(out, "  %-14s skipped (%s)\n", r.Name, r.Reason)
			case r.Error != "":
				fmt.Fprintf(out, "  %-14s error: %s\n", r.Name, r.Error)
			default:
				fmt.Fprintf(out, "  %-14s wired -> %s\n", r.Name, r.Launcher)
			}
		}
		if !d.Reachable && d.Tier == agentattach.TierAutoConnect {
			fmt.Fprintf(out, "\nLaunchers written, but Chrome's debug endpoint isn't live yet:\n%s\n", d.Remediation)
		}
	}

	if installed == 0 {
		return fmt.Errorf("no agent browser wired (none installed for --target %q)", attachTarget)
	}
	return nil
}

// wireOutcome is one target's wiring result, shared by text and JSON paths.
type wireOutcome struct {
	Name     string `json:"name"`
	Launcher string `json:"launcher,omitempty"`
	Note     string `json:"note,omitempty"`
	Skipped  bool   `json:"skipped,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Error    string `json:"error,omitempty"`
}

func nonEmptyIfUnreachable(d agentattach.Discovery) string {
	if d.Reachable {
		return ""
	}
	return d.Remediation
}

// attachStatusLine renders the one-line endpoint status header.
func attachStatusLine(d agentattach.Discovery) string {
	if d.Reachable {
		v := "unknown"
		if d.Version > 0 {
			v = fmt.Sprintf("%d", d.Version)
		}
		return fmt.Sprintf("Chrome reachable on CDP (version %s, tier %s).", v, d.Tier)
	}
	return fmt.Sprintf("Chrome debug endpoint not reachable (tier %s).", d.Tier)
}

// errNotReachable is returned (after printing) so `--check` exits non-zero
// for scripting without an extra error line.
var errNotReachable = errStr("chrome debug endpoint not reachable")

type errStr string

func (e errStr) Error() string { return string(e) }
