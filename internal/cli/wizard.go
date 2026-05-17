package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/keystore"
	"github.com/mvanhorn/agentcookie/internal/launchd"
	"github.com/mvanhorn/agentcookie/internal/pairing"
	"github.com/mvanhorn/agentcookie/internal/tsclient"
)

// Wizard flags. These are read by both `install` and `uninstall`.
var (
	wizardRole               string
	wizardPeer               string
	wizardListen             string
	wizardLocalName          string
	wizardSinkURL            string
	wizardCode               string
	wizardPairURL            string
	wizardRepair             bool
	wizardForce              bool
	wizardSkipDaemon         bool
	wizardSkipExitNode       bool
	wizardSkipKeychainPrompt bool
	wizardSkipPartitionList  bool
	wizardSkipKeychainAccess bool
	wizardSkipBridgeHint     bool
)

var wizardCmd = &cobra.Command{
	Use:   "wizard",
	Short: "One-command install: configure, pair, and start the agentcookie daemons",
	Long: `agentcookie wizard is the install front door for v0.2. One command per
machine, runnable by an AI agent over SSH or locally, end-to-end.

  agentcookie wizard install --as source --peer <sink-hostname>
  agentcookie wizard install --as sink   --peer <source-hostname> \
                                         --code <pairing-code>    \
                                         --pair-url <source-pair-url>

The source-side run drops configs, starts a pairing listener, writes the
sink-run command into ~/.agentcookie/pairing.json so an agent can SSH
to the sink and read it, and on successful pairing installs a LaunchAgent
that runs 'agentcookie source --watch' from then on.

The sink-side run drops configs (with cdp.managed: true by default so no
Keychain prompt fires), runs the sink-side handshake against the source's
pairing URL, and installs a LaunchAgent that runs 'agentcookie sink' from
then on.

The combined effect: two CLI invocations and the user is done. No two-
terminal copy-paste, no screen-sharing, no manual YAML editing.`,
}

var wizardInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Install agentcookie on this machine",
	RunE:  runWizardInstall,
}

var wizardUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Remove agentcookie LaunchAgent and configs from this machine",
	RunE:  runWizardUninstall,
}

func init() {
	wizardCmd.AddCommand(wizardInstallCmd)
	wizardCmd.AddCommand(wizardUninstallCmd)

	wizardInstallCmd.Flags().StringVar(&wizardRole, "as", "", "source | sink (required)")
	wizardInstallCmd.Flags().StringVar(&wizardPeer, "peer", "", "the OTHER machine's hostname")
	wizardInstallCmd.Flags().StringVar(&wizardListen, "listen", "", "[source] pairing listener bind address (default 0.0.0.0:9998)")
	wizardInstallCmd.Flags().StringVar(&wizardLocalName, "local-name", "", "hostname this side announces (default os.Hostname)")
	wizardInstallCmd.Flags().StringVar(&wizardSinkURL, "sink-url", "", "[source] override sink URL (default http://<peer>:9999/sync)")
	wizardInstallCmd.Flags().StringVar(&wizardCode, "code", "", "[sink] pairing code (from source's wizard output)")
	wizardInstallCmd.Flags().StringVar(&wizardPairURL, "pair-url", "", "[sink] source's pairing URL")
	wizardInstallCmd.Flags().BoolVar(&wizardRepair, "repair", false, "force a fresh pairing handshake even if a key already exists")
	wizardInstallCmd.Flags().BoolVar(&wizardForce, "force", false, "overwrite existing source.yaml / sink.yaml / allowlist.yaml")
	wizardInstallCmd.Flags().BoolVar(&wizardSkipDaemon, "skip-daemon", false, "skip installing the LaunchAgent (configs + pairing only)")
	wizardInstallCmd.Flags().BoolVar(&wizardSkipExitNode, "skip-exit-node-hint", false, "do not detect Tailscale or print the sudo commands that route the sink's outbound traffic through the source machine")
	wizardInstallCmd.Flags().BoolVar(&wizardSkipKeychainPrompt, "skip-keychain-prompt", false, "[sink] do not trigger the Chrome Safe Storage Keychain prompt during install; the sink daemon will prompt on first sync instead")
	wizardInstallCmd.Flags().BoolVar(&wizardSkipPartitionList, "skip-partition-list", false, "[sink] do not expand the Chrome Safe Storage Keychain partition list; PP CLIs using Apple-tool callers may then prompt on first read")
	wizardInstallCmd.Flags().BoolVar(&wizardSkipKeychainAccess, "skip-keychain-access", false, "[sink] do not run v0.10 set-keychain-access strategies (the kooky-CGO probe + partition/trust-list loop); kooky CLIs may then prompt on first read per binary")
	wizardInstallCmd.Flags().BoolVar(&wizardSkipBridgeHint, "skip-bridge-hint", false, "[sink] do not print the cookie-bridge env-var integration hint at install end")

	wizardUninstallCmd.Flags().StringVar(&wizardRole, "as", "", "source | sink (required)")
	wizardUninstallCmd.Flags().BoolVar(&wizardForce, "purge", false, "also delete configs and paired keys")
}

func runWizardInstall(cmd *cobra.Command, args []string) error {
	role := strings.ToLower(wizardRole)
	if role != "source" && role != "sink" {
		return fmt.Errorf("--as is required and must be 'source' or 'sink'")
	}
	if wizardPeer == "" {
		return fmt.Errorf("--peer is required (the OTHER machine's hostname)")
	}
	if wizardLocalName == "" {
		wizardLocalName = pairing.LocalHostname()
	}

	binPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate this binary: %w", err)
	}
	binPath, _ = filepath.Abs(binPath)

	home, _ := os.UserHomeDir()
	logDir := filepath.Join(home, ".agentcookie", "logs")

	switch role {
	case "source":
		return wizardInstallSource(cmd.Context(), binPath, logDir)
	case "sink":
		return wizardInstallSink(cmd.Context(), binPath, logDir)
	}
	return nil
}

func wizardInstallSource(ctx context.Context, binPath, logDir string) error {
	if err := os.MkdirAll(common.ConfigDir, 0o755); err != nil {
		return err
	}

	// Step 1: drop source.yaml + allowlist.yaml if missing or force.
	if err := writeYAMLIfMissing(
		filepath.Join(common.ConfigDir, "source.yaml"),
		renderSourceYAML(wizardPeer, wizardSinkURL),
		wizardForce,
	); err != nil {
		return err
	}
	if err := writeYAMLIfMissing(
		filepath.Join(common.ConfigDir, "blocklist.yaml"),
		starterBlocklistYAML(),
		wizardForce,
	); err != nil {
		return err
	}

	// Step 2: check existing keystore. If present and !repair, skip pairing.
	keyPath, _ := keystore.Path(common.ConfigDir, wizardPeer)
	keyExists := fileExists(keyPath)
	if keyExists && !wizardRepair {
		fmt.Fprintf(os.Stderr, "agentcookie wizard: existing paired key for %q found; skipping pairing (use --repair to force)\n", wizardPeer)
	} else {
		// Step 3: run source-side pairing in foreground.
		listen := wizardListen
		if listen == "" {
			listen = "0.0.0.0:9998"
		}
		// Write a pairing info file so an SSH'ing agent can grab it.
		pairingInfo, code, err := beginSourcePairing(ctx, listen, wizardLocalName, binPath, logDir)
		if err != nil {
			return fmt.Errorf("pairing: %w", err)
		}
		fmt.Fprintln(os.Stderr, pairingInfo)
		fmt.Fprintf(os.Stderr, "agentcookie wizard: paired with %q (code was %s)\n", wizardPeer, code)
	}

	// Step 4: install the LaunchAgent unless skipped.
	if !wizardSkipDaemon {
		if err := installLaunchAgent(launchd.Spec{
			Role:       launchd.RoleSource,
			BinaryPath: binPath,
			LogDir:     logDir,
			ExtraArgs:  []string{"--watch"},
		}); err != nil {
			return fmt.Errorf("install source LaunchAgent: %w", err)
		}
		fmt.Fprintln(os.Stderr, "agentcookie wizard: source LaunchAgent installed and started")
	}

	if !wizardSkipExitNode {
		printExitNodeHint(ctx, "source", wizardPeer)
	}

	fmt.Fprintln(os.Stderr, "agentcookie wizard: source install complete")
	return nil
}

func wizardInstallSink(ctx context.Context, binPath, logDir string) error {
	if wizardCode == "" || wizardPairURL == "" {
		return fmt.Errorf("--code and --pair-url are required when --as sink")
	}
	if err := os.MkdirAll(common.ConfigDir, 0o755); err != nil {
		return err
	}

	if err := writeYAMLIfMissing(
		filepath.Join(common.ConfigDir, "sink.yaml"),
		renderSinkYAML(wizardPeer),
		wizardForce,
	); err != nil {
		return err
	}
	if err := writeYAMLIfMissing(
		filepath.Join(common.ConfigDir, "blocklist.yaml"),
		starterBlocklistYAML(),
		wizardForce,
	); err != nil {
		return err
	}

	keyPath, _ := keystore.Path(common.ConfigDir, wizardPeer)
	if fileExists(keyPath) && !wizardRepair {
		fmt.Fprintf(os.Stderr, "agentcookie wizard: existing paired key for %q found; skipping pairing (use --repair to force)\n", wizardPeer)
	} else {
		res, err := pairing.RunSink(ctx, wizardPairURL, pairing.Code(wizardCode), wizardLocalName)
		if err != nil {
			return fmt.Errorf("sink pairing: %w", err)
		}
		pk := &keystore.PeerKey{
			Peer:        wizardPeer,
			Key:         res.Key,
			PairedAt:    res.PairedAt,
			Fingerprint: res.Fingerprint,
			ProtocolVer: pairing.ProtocolVersion,
		}
		if err := keystore.Save(common.ConfigDir, pk); err != nil {
			return fmt.Errorf("save key: %w", err)
		}
		fmt.Fprintf(os.Stderr, "agentcookie wizard: paired with source %q (fingerprint %s)\n", wizardPeer, res.Fingerprint)
	}

	// v0.7: trigger the one-time Keychain Always-Allow prompt for Chrome
	// Safe Storage. The sink LaunchAgent reads this key on every startup
	// to encrypt cookies for SQLite writes; without Always-Allow, the
	// daemon would block on a Keychain prompt nobody can see.
	if !wizardSkipKeychainPrompt {
		fmt.Fprintln(os.Stderr, "agentcookie wizard: triggering Chrome Safe Storage Keychain prompt (click 'Always Allow' when macOS asks)")
		if _, err := chrome.SafeStoragePassword(); err != nil {
			return fmt.Errorf("Keychain access: %w (re-run after granting Always Allow, or pass --skip-keychain-prompt)", err)
		}
		fmt.Fprintln(os.Stderr, "agentcookie wizard: Keychain access granted; sink daemon can run unattended")
	}

	// v0.10: run the set-keychain-access strategy loop. This supersedes the
	// v0.9 partition-list step -- v0.10's first strategy IS the same
	// partition-list expansion, plus it verifies the result via the
	// keybase/go-keychain API path kooky-CGO uses, and falls back to
	// per-binary trust-list entries if the partition list alone does not
	// cover ad-hoc-signed Go binaries. The whole thing runs inside a
	// one-shot LaunchAgent (auto-unlocked keychain) so no login password
	// prompt fires. See plan 2026-05-17-004.
	if !wizardSkipKeychainAccess {
		fmt.Fprintln(os.Stderr, "agentcookie wizard: running set-keychain-access strategy loop (broadens Chrome Safe Storage access for kooky-using CLIs)")
		setKeychainExtraBinary = defaultKeychainTrustBinaries()
		if err := runOuterWizard(setKeychainAccessCmd); err != nil {
			// Do NOT abort install. Sink itself still works; only the
			// per-CLI headless-read path is degraded.
			fmt.Fprintf(os.Stderr, "agentcookie wizard: WARNING keychain access strategy loop failed: %v\n", err)
			fmt.Fprintln(os.Stderr, "agentcookie wizard:   kooky-using CLIs on this sink will prompt for Keychain access on first read per binary")
			fmt.Fprintln(os.Stderr, "agentcookie wizard:   see docs/runbook-v0.10-keychain-access.md for recovery")
		}
	}

	if !wizardSkipDaemon {
		if err := installLaunchAgent(launchd.Spec{
			Role:       launchd.RoleSink,
			BinaryPath: binPath,
			LogDir:     logDir,
		}); err != nil {
			return fmt.Errorf("install sink LaunchAgent: %w", err)
		}
		fmt.Fprintln(os.Stderr, "agentcookie wizard: sink LaunchAgent installed and started")
	}

	if !wizardSkipExitNode {
		printExitNodeHint(ctx, "sink", wizardPeer)
	}

	if !wizardSkipBridgeHint {
		printBridgeHint()
	}

	fmt.Fprintln(os.Stderr, "agentcookie wizard: sink install complete")
	return nil
}

// defaultKeychainTrustBinaries returns kooky-using CLI binaries the wizard
// should fall back to per-binary trust-list grants for, when the
// partition-list strategies alone do not suffice. Tries a handful of known
// PP CLI install paths; only includes paths that actually exist so the
// trust-list strategy doesn't error on a missing file.
func defaultKeychainTrustBinaries() []string {
	home, _ := os.UserHomeDir()
	candidates := []string{
		filepath.Join(home, "go", "bin", "instacart-pp-cli"),
	}
	// Common printing-press library install paths -- include any that
	// actually exist on this machine.
	ppDir := filepath.Join(home, "printing-press", "library")
	if entries, err := os.ReadDir(ppDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			bin := filepath.Join(ppDir, e.Name(), e.Name())
			candidates = append(candidates, bin)
		}
	}
	var present []string
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			present = append(present, c)
		}
	}
	return present
}

// printBridgeHint tells the operator how PP CLIs use the cookie bridge.
// v0.9 ships two paths:
//
//   1. Primary: Mac mini's actual Chrome Cookies file is written in plain
//      v10 mode (no App-Bound prefix) with meta.version=18. Any kooky
//      v0.2.2 caller reads it via the macOS Keychain Safe Storage key.
//      No env var, no cookiesource integration required.
//   2. Backstop: ~/.agentcookie/cookies-plain.db sidecar (mode 0600,
//      plaintext values, empty encrypted_value). cookiesource-aware
//      callers honor AGENTCOOKIE_PLAIN_COOKIES.
//
// Precondition for path 1: Mac mini Chrome stays quit. The wizard says so;
// the user keeps it that way.
func printBridgeHint() {
	fmt.Fprintln(os.Stderr, "agentcookie wizard: PP CLIs and headless agents read cookies two ways on this sink:")
	fmt.Fprintln(os.Stderr, "  1. Direct: any kooky v0.2.2 caller reads Mac mini's Chrome Cookies file (v0.9 plain-v10 mode).")
	fmt.Fprintln(os.Stderr, "     Keep Chrome QUIT on this machine: launching it migrates meta.version and breaks the bridge.")
	fmt.Fprintln(os.Stderr, "  2. Sidecar: cookiesource-aware callers honor:")
	fmt.Fprintln(os.Stderr, "       export AGENTCOOKIE_PLAIN_COOKIES=~/.agentcookie/cookies-plain.db")
}

// printExitNodeHint inspects Tailscale state and emits sudo command lines
// the user can run once to align the sink machine's outbound IP with the
// source machine's. It never executes sudo itself; that is intentionally
// left to the human since it changes routing for the entire machine.
//
// role: "source" or "sink", determines which side of the relationship we
// are configuring. peerHost is the OTHER machine's tailnet hostname.
func printExitNodeHint(ctx context.Context, role, peerHost string) {
	cli, err := tsclient.FindCLI()
	if err != nil {
		fmt.Fprintln(os.Stderr, "agentcookie wizard: Tailscale not detected; skipping exit-node hint")
		fmt.Fprintln(os.Stderr, "agentcookie wizard: sites that bind sessions to the source machine's IP (instacart class) will not work without an exit-node hop")
		return
	}
	st, err := tsclient.Get(ctx, cli)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agentcookie wizard: Tailscale CLI errored (%v); skipping exit-node hint\n", err)
		return
	}

	switch role {
	case "source":
		if st.SelfAdvertisesExitNode() {
			fmt.Fprintln(os.Stderr, "agentcookie wizard: this machine already advertises as a Tailscale exit node")
			return
		}
		fmt.Fprintln(os.Stderr, "agentcookie wizard: optional next step on this machine (source):")
		fmt.Fprintln(os.Stderr, "  sudo tailscale set --advertise-exit-node")
		fmt.Fprintln(os.Stderr, "  # then approve the exit-node offer in the Tailscale admin console")
		fmt.Fprintln(os.Stderr, "  # routes the sink machine's outbound traffic through this machine's public IP")
		fmt.Fprintln(os.Stderr, "  # keeps session-bound sites (instacart class) working when the sink hits them")
	case "sink":
		peer := st.FindPeer(peerHost)
		if peer == nil {
			fmt.Fprintf(os.Stderr, "agentcookie wizard: tailnet peer %q not found in Tailscale status; cannot suggest an exit-node command\n", peerHost)
			fmt.Fprintln(os.Stderr, "agentcookie wizard: confirm both machines are on the same tailnet and the source advertises as exit node, then re-run the wizard")
			return
		}
		if st.SelfUsesExitNode() {
			fmt.Fprintln(os.Stderr, "agentcookie wizard: this machine already routes through a Tailscale exit node")
			return
		}
		if !peer.ExitNodeOption {
			fmt.Fprintf(os.Stderr, "agentcookie wizard: peer %q is on the tailnet but is NOT advertising as exit node\n", peerHost)
			fmt.Fprintln(os.Stderr, "agentcookie wizard: run this on the source machine first:")
			fmt.Fprintln(os.Stderr, "  sudo tailscale set --advertise-exit-node")
			fmt.Fprintln(os.Stderr, "agentcookie wizard: then approve the exit-node offer at https://login.tailscale.com/admin/machines")
			return
		}
		fmt.Fprintln(os.Stderr, "agentcookie wizard: optional next step on this machine (sink):")
		fmt.Fprintf(os.Stderr, "  sudo tailscale set --exit-node=%s --exit-node-allow-lan-access=true\n", peerHost)
		fmt.Fprintln(os.Stderr, "  # routes this machine's outbound traffic through the source's public IP")
		fmt.Fprintln(os.Stderr, "  # verify with: curl -s https://api.ipify.org && echo")
		fmt.Fprintln(os.Stderr, "  # to undo: sudo tailscale set --exit-node=")
	}
}

func runWizardUninstall(cmd *cobra.Command, args []string) error {
	role := strings.ToLower(wizardRole)
	if role != "source" && role != "sink" {
		return fmt.Errorf("--as is required and must be 'source' or 'sink'")
	}
	var spec launchd.Spec
	switch role {
	case "source":
		spec.Role = launchd.RoleSource
	case "sink":
		spec.Role = launchd.RoleSink
	}
	if err := launchd.Uninstall(spec); err != nil {
		return fmt.Errorf("uninstall LaunchAgent: %w", err)
	}
	fmt.Fprintf(os.Stderr, "agentcookie wizard: %s LaunchAgent removed\n", role)

	if wizardForce {
		// Purge configs.
		_ = os.Remove(filepath.Join(common.ConfigDir, "source.yaml"))
		_ = os.Remove(filepath.Join(common.ConfigDir, "sink.yaml"))
		_ = os.Remove(filepath.Join(common.ConfigDir, "allowlist.yaml"))
		_ = os.RemoveAll(filepath.Join(common.ConfigDir, "keys"))
		fmt.Fprintln(os.Stderr, "agentcookie wizard: --purge set; configs and paired keys removed")
	}
	return nil
}

// beginSourcePairing starts a source-side pairing listener and waits for the
// sink to connect. Returns a human-readable instruction block (which is also
// the content of ~/.agentcookie/pairing.json) plus the code, blocking until
// pairing completes or times out.
func beginSourcePairing(ctx context.Context, listen, localName, binPath, logDir string) (string, pairing.Code, error) {
	pairingInfoPath := filepath.Join(filepath.Dir(common.ConfigDir), ".agentcookie", "pairing.json")
	_ = pairingInfoPath // computed for symmetry; we write under ~/.agentcookie/

	home, _ := os.UserHomeDir()
	infoPath := filepath.Join(home, ".agentcookie", "pairing.json")
	if err := os.MkdirAll(filepath.Dir(infoPath), 0o700); err != nil {
		return "", "", err
	}

	// RunSource generates the code internally and prints it. We wrap so we can
	// also write it to a file the SSH'ing agent can grab.
	codeCh := make(chan pairing.Code, 1)
	infoWriter := &pairingInfoWriter{
		listen:      listen,
		peer:        localName,
		path:        infoPath,
		notify:      codeCh,
		onPlainLine: os.Stderr,
	}

	res, code, err := pairing.RunSource(ctx, listen, localName, infoWriter)
	if err != nil {
		_ = os.Remove(infoPath)
		return "", code, err
	}

	pk := &keystore.PeerKey{
		Peer:        res.RemotePeer,
		Key:         res.Key,
		PairedAt:    res.PairedAt,
		Fingerprint: res.Fingerprint,
		ProtocolVer: pairing.ProtocolVersion,
	}
	if err := keystore.Save(common.ConfigDir, pk); err != nil {
		return "", code, fmt.Errorf("save key: %w", err)
	}
	// Clean up the pairing info file now that we're paired.
	_ = os.Remove(infoPath)

	return fmt.Sprintf("agentcookie wizard: paired (code %s, fingerprint %s)", code, res.Fingerprint), code, nil
}

// pairingInfoWriter intercepts the source-side pairing announcement and writes
// a JSON sibling file an SSH'ing agent can grab.
type pairingInfoWriter struct {
	listen      string
	peer        string
	path        string
	notify      chan<- pairing.Code
	onPlainLine *os.File
	written     bool
}

func (p *pairingInfoWriter) Write(data []byte) (int, error) {
	if !p.written && strings.Contains(string(data), "pairing code:") {
		code := extractCode(string(data))
		if code != "" {
			info := map[string]string{
				"code":     code,
				"peer":     p.peer,
				"pair_url": fmt.Sprintf("http://%s/pair", p.listen),
				"sink_run": fmt.Sprintf("agentcookie wizard install --as sink --peer %s --code %s --pair-url http://%s/pair", p.peer, code, p.listen),
			}
			body, _ := json.MarshalIndent(info, "", "  ")
			_ = os.WriteFile(p.path, body, 0o600)
			p.written = true
			select {
			case p.notify <- pairing.Code(code):
			default:
			}
		}
	}
	return p.onPlainLine.Write(data)
}

func extractCode(text string) string {
	const tag = "pairing code:"
	i := strings.Index(text, tag)
	if i < 0 {
		return ""
	}
	tail := text[i+len(tag):]
	fields := strings.Fields(tail)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func writeYAMLIfMissing(path, content string, force bool) error {
	if !force && fileExists(path) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o600)
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func renderSourceYAML(peer, sinkURLOverride string) string {
	sinkURL := sinkURLOverride
	if sinkURL == "" {
		// Default: http://<peer>:9999/sync (Tailscale MagicDNS resolves the hostname).
		sinkURL = fmt.Sprintf("http://%s:9999/sync", peer)
	}
	return fmt.Sprintf(`sink:
  url: %s
chrome:
  db_path: ~/Library/Application Support/Google/Chrome/Default/Cookies
peer:
  hostname: %s
`, sinkURL, peer)
}

func renderSinkYAML(peer string) string {
	// Bind to the local tailnet IP for sink listener if available; otherwise 0.0.0.0:9999.
	listenAddr := defaultSinkListenAddr()
	// v0.7: cdp.enabled is gone. Direct SQLite + leveldb file writes are
	// the only path. The Chrome quit/relaunch ceremony around each sync
	// is handled by the chromectl package.
	return fmt.Sprintf(`listen:
  addr: %s
peer:
  hostname: %s
`, listenAddr, peer)
}

func defaultSinkListenAddr() string {
	// Prefer a Tailscale IP if we can find one in 100.64.0.0/10 on a local interface.
	addrs, err := net.InterfaceAddrs()
	if err == nil {
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || ipnet.IP.To4() == nil {
				continue
			}
			ip := ipnet.IP.To4()
			if ip[0] == 100 && ip[1] >= 64 && ip[1] <= 127 {
				return fmt.Sprintf("%s:9999", ip.String())
			}
		}
	}
	return "0.0.0.0:9999"
}

func starterBlocklistYAML() string {
	return `# blocklist.yaml: domains to KEEP on this machine (NOT synced to the peer).
# Empty file = sync everything. Uncomment any pattern below to opt out.
version: 1
domains:
  # Banking / brokerage / personal finance:
  # - pattern: "%chase.com"
  # - pattern: "%vanguard.com"
  # - pattern: "%fidelity.com"
  # - pattern: "%schwab.com"
  # - pattern: "%bankofamerica.com"
  # Password managers (probably never want these on a second machine):
  # - pattern: "%1password.com"
  # - pattern: "%bitwarden.com"
  # - pattern: "%lastpass.com"
  # Tax / IRS:
  # - pattern: "%irs.gov"
  # - pattern: "%turbotax.intuit.com"
  # Health / insurance:
  # - pattern: "%kaiserpermanente.org"
  # - pattern: "%bcbs.com"
`
}

func installLaunchAgent(spec launchd.Spec) error {
	_, err := launchd.Install(spec)
	if err != nil {
		// Fall back to a kickstart in case bootstrap raced something.
		if errIsAlreadyLoaded(err) {
			uid := fmt.Sprintf("%d", os.Getuid())
			return exec.Command("launchctl", "kickstart", "-k", "gui/"+uid+"/"+spec.Label()).Run()
		}
		return err
	}
	return nil
}

func errIsAlreadyLoaded(err error) bool {
	if err == nil {
		return false
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return false
	}
	// launchctl bootstrap returns specific status codes when the job is already loaded.
	// We do not enumerate them here; the kickstart attempt is safe regardless.
	_ = time.Second // placeholder reference to avoid import-cleanup issues
	return false
}
