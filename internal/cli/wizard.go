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

	"github.com/mvanhorn/agentcookie/internal/config"
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

	// v0.12.0-beta.3: headless-sink mode flags. Default to "" so we can
	// distinguish "user did not set" from "user set explicitly". When
	// neither is set, the wizard auto-detects via `isHeadlessInstall()`.
	wizardSkipChromeSQLite  bool
	wizardWriteChromeSQLite bool
	wizardNoCDP             bool
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
	wizardInstallCmd.Flags().BoolVar(&wizardForce, "force", false, "overwrite existing source.yaml / sink.yaml / blocklist.yaml")
	wizardInstallCmd.Flags().BoolVar(&wizardSkipDaemon, "skip-daemon", false, "skip installing the LaunchAgent (configs + pairing only)")
	wizardInstallCmd.Flags().BoolVar(&wizardSkipExitNode, "skip-exit-node-hint", false, "do not detect Tailscale or print the sudo commands that route the sink's outbound traffic through the source machine")
	wizardInstallCmd.Flags().BoolVar(&wizardSkipKeychainPrompt, "skip-keychain-prompt", false, "[sink] do not trigger the Chrome Safe Storage Keychain prompt during install; the sink daemon will prompt on first sync instead")
	wizardInstallCmd.Flags().BoolVar(&wizardSkipPartitionList, "skip-partition-list", false, "[sink] do not expand the Chrome Safe Storage Keychain partition list; PP CLIs using Apple-tool callers may then prompt on first read")
	wizardInstallCmd.Flags().BoolVar(&wizardSkipKeychainAccess, "skip-keychain-access", false, "[sink] do not run v0.10 set-keychain-access strategies (the kooky-CGO probe + partition/trust-list loop); kooky CLIs may then prompt on first read per binary")
	wizardInstallCmd.Flags().BoolVar(&wizardSkipBridgeHint, "skip-bridge-hint", false, "[sink] do not print the cookie-bridge env-var integration hint at install end")
	wizardInstallCmd.Flags().BoolVar(&wizardSkipChromeSQLite, "skip-chrome-sqlite", false, "[sink] opt OUT of universal delivery: the sink daemon never reads Chrome Safe Storage or writes Chrome SQLite/leveldb; sidecar + adapter push remain the cookie-delivery paths (degraded mode). The v0.13 default is universal regardless of TTY; pass this to force degraded. Overrides --write-chrome-sqlite when both passed.")
	wizardInstallCmd.Flags().BoolVar(&wizardWriteChromeSQLite, "write-chrome-sqlite", false, "[sink] force universal delivery (write the real Default Chrome profile) and honor it even if the one-password keychain open cannot complete; does not silently downgrade to degraded")
	wizardInstallCmd.Flags().BoolVar(&wizardNoCDP, "no-cdp", false, "[sink] do not enable CDP injection alongside skip_chrome_sqlite. By default, headless installs enable CDP injection so Chrome on the sink still sees synced cookies. Pass --no-cdp for sidecar+adapter-only mode.")

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

	// Step 1: drop source.yaml + blocklist.yaml if missing or force.
	// v0.12.0-beta.2: if source.yaml already exists with a peer.hostname
	// that differs from --peer, fail loud rather than silently keeping the
	// stale value (per friction log #14, 2026-05-19 dry-run). A future
	// pair handshake would then save the new key under wizardPeer while
	// the running daemon would keep looking up the stale name -> silent
	// sync failure.
	if err := guardConfigPeerMismatch("source", filepath.Join(common.ConfigDir, "source.yaml"), wizardPeer); err != nil {
		return err
	}
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
		// v0.12 S1: refuse 0.0.0.0. The pair listener is reached over
		// the tailnet; binding on every interface makes a flaky
		// detection at install time silently turn a personal pair
		// endpoint into an internet-reachable one. The wizard now
		// fails loud if Tailscale isn't usable.
		listen := wizardListen
		if listen == "" {
			ip, err := tsclient.RequireTailnetIP(ctx)
			if err != nil {
				return fmt.Errorf("detect Tailscale 100.x address for pair listener: %w", err)
			}
			listen = fmt.Sprintf("%s:9998", ip)
		} else if err := validateListenAddr(listen); err != nil {
			return fmt.Errorf("--listen %q: %w", listen, err)
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

	// v0.12.0-beta.6: resolve headless mode ONCE at the top of wizard
	// install so the result gates both the sink.yaml render AND the
	// downstream keychain prompt + strategy loop. Pre-beta.6 the
	// resolve fired only inside the "write fresh sink.yaml" branch,
	// which meant upgrade-in-place installs always ran the strategy
	// loop even on headless sinks where the daemon never touches
	// Chrome Safe Storage. That produced the 60-second timeout +
	// alarming WARNING block documented as friction #19 in the
	// 2026-05-19 dry-run.
	skipChromeSQLite, cdpEnabled, cdpProfileDir, delivery := resolveSinkHeadlessMode()

	// v0.13 (plan 2026-05-31-002, R5): for the default/universal-intent
	// case the KEYCHAIN-OPEN OUTCOME determines the final delivery mode,
	// so it must run BEFORE we render sink.yaml. A universal config
	// (skip_chrome_sqlite=false) requires agentcookie to read the Chrome
	// Safe Storage key (the sink daemon writes the real Default profile
	// and reads the key to do so; see sink.go). On a box where
	// agentcookie is not yet keychain-trusted (or has no GUI session),
	// the any-app open fails -> we must DOWNGRADE to degraded so the
	// rendered sink.yaml does not leave a daemon that cannot start.
	//
	// We perform the open up front (gated below), then re-derive the
	// skip/cdp/delivery values from its outcome for the render.
	skipChromeSQLite, cdpEnabled, cdpProfileDir, delivery, keychainOpened := resolveSinkDeliveryWithKeychain(
		skipChromeSQLite, cdpEnabled, cdpProfileDir, delivery,
	)

	// v0.12 S1: resolve the tailnet 100.x address BEFORE writing
	// sink.yaml. If Tailscale is not up the helper returns a
	// structured error and we refuse to write a permissive default.
	// On an existing v0.11 install the sink.yaml already has a
	// concrete 100.x address; writeYAMLIfMissing skips re-writing
	// it unless --force is set, so this detection only fires for
	// fresh installs and explicit --force overwrites.
	sinkYAMLPath := filepath.Join(common.ConfigDir, "sink.yaml")
	// v0.12.0-beta.2: if sink.yaml already exists with a peer.hostname
	// that differs from --peer, fail loud rather than silently keeping
	// the stale value (per friction log #14, 2026-05-19 dry-run).
	if err := guardConfigPeerMismatch("sink", sinkYAMLPath, wizardPeer); err != nil {
		return err
	}
	if wizardForce || !fileExists(sinkYAMLPath) {
		ip, err := tsclient.RequireTailnetIP(ctx)
		if err != nil {
			return fmt.Errorf("detect Tailscale 100.x address for sink listener: %w", err)
		}
		listenAddr := fmt.Sprintf("%s:9999", ip)
		// Headless mode + CDP injection resolved above; use the values
		// for the YAML render here.
		if cdpEnabled {
			if err := os.MkdirAll(expandHome(cdpProfileDir), 0o700); err != nil {
				return fmt.Errorf("create CDP profile dir %s: %w", cdpProfileDir, err)
			}
		}
		if err := writeYAMLIfMissing(
			sinkYAMLPath,
			renderSinkYAML(wizardPeer, listenAddr, skipChromeSQLite, cdpEnabled, cdpProfileDir, delivery),
			wizardForce,
		); err != nil {
			return err
		}
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

	// v0.13: the keychain open already ran (above, before sink.yaml was
	// rendered) so its outcome could drive the skip/cdp/delivery render.
	// All the keychain prompt + strategy-loop logic now lives in
	// resolveSinkDeliveryWithKeychain. Nothing to do here; keychainOpened
	// recorded whether the universal any-app open succeeded.
	_ = keychainOpened

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
// v0.11's adapter pattern is the primary path: after each sync, the
// sink writes per-CLI session caches directly. v0.9's plain-v10 + v0.8
// sidecar remain as fallbacks for kooky-using CLIs that have no
// registered adapter.
func printBridgeHint() {
	fmt.Fprintln(os.Stderr, "agentcookie wizard: PP CLIs read cookies on this sink via three paths:")
	fmt.Fprintln(os.Stderr, "  1. Adapter push (v0.11): the sink writes each registered PP CLI's session cache after every sync.")
	fmt.Fprintln(os.Stderr, "     Verify with: agentcookie wizard verify-adapters")
	fmt.Fprintln(os.Stderr, "     Five built-ins ship: instacart, airbnb, ebay, pagliacci, table-reservation-goat.")
	fmt.Fprintln(os.Stderr, "  2. Plain v10 Chrome cookies (v0.9): kooky-using CLIs with no adapter can still read from")
	fmt.Fprintln(os.Stderr, "     Chrome's actual Cookies file. Keep Chrome QUIT on this machine to preserve the format.")
	fmt.Fprintln(os.Stderr, "  3. Sidecar (v0.8): cookiesource-aware callers honor:")
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

	// v0.12.0-beta.2: file the key under the operator-supplied peer
	// name (--peer / wizardPeer), not the sink's announced os.Hostname()
	// returned in res.RemotePeer. source.yaml stores the operator-supplied
	// name; if the key file is saved under the announced name (often a
	// Bonjour FQDN like "moltbot-mini.hsd1.wa.comcast.net") then the
	// running source LaunchAgent looks up the wrong filename at sync time
	// and reports connection-refused with no hint that the failure is
	// actually a missing key. The announced name is still recorded in
	// the saved PeerKey for diagnostics. See friction log #17 (dry-run
	// 2026-05-19).
	pk := &keystore.PeerKey{
		Peer:        wizardPeer,
		AnnouncedAs: res.RemotePeer,
		Key:         res.Key,
		PairedAt:    res.PairedAt,
		Fingerprint: res.Fingerprint,
		ProtocolVer: pairing.ProtocolVersion,
	}
	if wizardPeer != res.RemotePeer {
		fmt.Fprintf(os.Stderr, "agentcookie wizard: sink announced itself as %q; storing key under operator-supplied --peer %q\n", res.RemotePeer, wizardPeer)
	}
	if err := keystore.Save(common.ConfigDir, pk); err != nil {
		return "", code, fmt.Errorf("save key: %w", err)
	}
	// Clean up the pairing info file now that we're paired.
	_ = os.Remove(infoPath)

	return fmt.Sprintf("agentcookie wizard: paired (code %s, fingerprint %s)", code, res.Fingerprint), code, nil
}

// guardConfigPeerMismatch refuses to leave a stale peer.hostname in
// place when the operator has explicitly passed a different --peer.
// If the existing config has a matching peer or no peer set, returns
// nil. If they differ and --force isn't passed, returns an error
// pointing at remediation.
func guardConfigPeerMismatch(role, path, wantPeer string) error {
	if !fileExists(path) {
		return nil
	}
	var existing string
	switch role {
	case "source":
		cfg, err := config.LoadSource(filepath.Dir(path))
		if err != nil {
			return nil // let writeYAMLIfMissing decide what to do
		}
		existing = cfg.Peer.Hostname
	case "sink":
		cfg, err := config.LoadSink(filepath.Dir(path))
		if err != nil {
			return nil
		}
		existing = cfg.Peer.Hostname
	default:
		return nil
	}
	if existing == "" || existing == wantPeer {
		return nil
	}
	if wizardForce {
		fmt.Fprintf(os.Stderr, "agentcookie wizard: rewriting %s.yaml peer.hostname %q -> %q (--force)\n", role, existing, wantPeer)
		return nil
	}
	return fmt.Errorf("existing %s.yaml has peer.hostname %q but --peer is %q; pass --force to overwrite (otherwise pair handshake will save a key the running daemon cannot find)", role, existing, wantPeer)
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

// renderSinkYAML formats sink.yaml with a caller-resolved listen
// address. The wizard install path is the only place that calls this,
// and it resolves listenAddr via tsclient.RequireTailnetIP first so a
// detection failure refuses to write sink.yaml rather than silently
// falling through to a permissive default. See v0.12 S1.
//
// v0.12.0-beta.3 added skipChromeSQLite + CDP injection. When
// skipChromeSQLite is false (the legacy default), the rendered YAML
// matches the pre-beta.3 shape byte-for-byte — that's the regression
// guard for installed v0.12.0-beta.2 friends (R6 in plan
// 2026-05-21-001).
//
// v0.13 added the delivery marker. It is appended only when non-empty so
// the legacy byte-for-byte shape (skip=false, cdp=false, delivery="")
// stays a regression-stable target; the wizard install always passes a
// concrete "universal"/"degraded" value, while tests and callers that
// pass "" keep the pre-v0.13 output.
func renderSinkYAML(peer, listenAddr string, skipChromeSQLite, cdpEnabled bool, cdpProfileDir, delivery string) string {
	out := fmt.Sprintf(`listen:
  addr: %s
peer:
  hostname: %s
`, listenAddr, peer)
	if skipChromeSQLite {
		out += "skip_chrome_sqlite: true\n"
	}
	if cdpEnabled {
		out += "cdp:\n  enabled: true\n"
		if cdpProfileDir != "" {
			out += fmt.Sprintf("  profile_dir: %s\n", cdpProfileDir)
		}
	}
	if delivery != "" {
		out += fmt.Sprintf("delivery: %s\n", delivery)
	}
	return out
}

// isHeadlessInstall returns true when stdin is not a terminal, which
// signals an SSH-only install with no GUI session to answer Keychain
// prompts. Pre-v0.13 the wizard used this to auto-degrade no-TTY sink
// installs (skip_chrome_sqlite + cdp). v0.13 made universal the default
// regardless of TTY (the degraded fallback now happens non-fatally at the
// keychain step), so resolveSinkHeadlessMode no longer consults this.
// Retained as a small TTY probe for callers and tests. When in doubt
// (e.g. tests), returns false.
func isHeadlessInstall() bool {
	stat, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (stat.Mode() & os.ModeCharDevice) == 0
}

// Delivery markers recorded in sink.yaml so doctor reports the INTENT a
// wizard install resolved to, rather than re-inferring it. See
// config.SinkConfig.Delivery.
const (
	deliveryUniversal = "universal"
	deliveryDegraded  = "degraded"
)

// resolveSinkHeadlessMode applies the v0.13 universal-default resolution
// rules. Returns (skipChromeSQLite, cdpEnabled, cdpProfileDir, delivery).
//
// v0.13 change: a plain `wizard install --as sink` (neither
// --skip-chrome-sqlite nor --write-chrome-sqlite passed) now defaults to
// UNIVERSAL delivery -- skip=false, write the real Default Chrome profile,
// and (at the keychain step) the any-app open -- so any unmodified cookie
// tool works on the sink. The pre-v0.13 no-TTY auto-degrade is gone:
// headless SSH installs default to universal too, and only fall back to
// degraded non-fatally if the any-app keychain open cannot complete (see
// the keychain block in wizardInstallSink, which prints the one-line
// upgrade instruction without failing the install).
//
// Precedence:
//  1. --skip-chrome-sqlite explicit -> skip=true (degraded opt-out).
//  2. --write-chrome-sqlite explicit -> skip=false (universal).
//  3. Neither flag -> skip=false (universal DEFAULT, the v0.13 change).
//
// CDP defaults: enabled when skip=true and --no-cdp is not set.
func resolveSinkHeadlessMode() (skipChromeSQLite, cdpEnabled bool, cdpProfileDir, delivery string) {
	switch {
	case wizardSkipChromeSQLite:
		skipChromeSQLite = true
	case wizardWriteChromeSQLite:
		skipChromeSQLite = false
	default:
		// v0.13: universal is the default when neither flag is passed,
		// independent of TTY. The degraded fallback for a never-trusted
		// box happens at the keychain step, non-fatally.
		skipChromeSQLite = false
	}
	if skipChromeSQLite && !wizardNoCDP {
		cdpEnabled = true
		cdpProfileDir = "~/.agentcookie/chrome-profile"
	}
	if skipChromeSQLite {
		delivery = deliveryDegraded
	} else {
		delivery = deliveryUniversal
	}
	return
}

// attemptUniversalKeychainOpen performs the v0.13 universal Chrome Safe
// Storage open. As of the one-password onboarding change this is the inline
// partition-list path: it prompts for the login password once (or reads
// AGENTCOOKIE_LOGIN_PASSWORD), sets the partition list with that password
// via `security -k`, and never deletes or rewrites the Safe Storage item.
// It runs cleanly over SSH with no GUI SecurityAgent prompt.
//
// It returns an error when the open could not complete (typically: no login
// password is available because the install is fully non-interactive with no
// AGENTCOOKIE_LOGIN_PASSWORD set, or the `security` partition set failed). On
// that error the caller downgrades to degraded non-fatally.
//
// It is a function variable so tests can inject success/failure without
// touching the real Keychain (mirrors execSecurityFunc in
// wizard_keychain.go).
var attemptUniversalKeychainOpen = func() error {
	return runInlinePartitionAccess()
}

// resolveSinkDeliveryWithKeychain takes the flag-resolved delivery mode
// (from resolveSinkHeadlessMode) and applies the v0.13 keychain-open
// outcome (plan 2026-05-31-002, R5). The keychain-open result determines
// the FINAL delivery mode for the default/universal-intent case, so this
// runs BEFORE sink.yaml is rendered.
//
// Behavior by intent:
//   - Explicit --skip-chrome-sqlite (degraded opt-out): no keychain open
//     attempted; stays degraded. Matches the old "skip the loop" branch.
//   - Explicit --skip-keychain-access: no open attempted; the caller asked
//     us not to touch the keychain. Stays as resolved (universal config but
//     keychain left untouched -- explicit operator choice).
//   - Explicit --write-chrome-sqlite (forced universal): attempt the open;
//     if it fails, surface a clear WARNING but HONOR the explicit intent --
//     do NOT silently downgrade. Universal config is rendered regardless.
//   - Default (neither --skip-chrome-sqlite nor --write-chrome-sqlite): the
//     universal-intent case. Attempt the open. On SUCCESS -> universal. On
//     FAILURE -> DOWNGRADE to degraded (skip=true, CDP enabled like the old
//     headless mode), print the one-line upgrade instruction, and continue
//     NON-FATALLY so the install completes with a working sink.
//
// Returns the (possibly downgraded) skip/cdp/profileDir/delivery to render,
// plus whether the universal any-app open actually succeeded.
func resolveSinkDeliveryWithKeychain(
	skipChromeSQLite, cdpEnabled bool, cdpProfileDir, delivery string,
) (outSkip, outCDP bool, outProfileDir, outDelivery string, keychainOpened bool) {
	// Degraded opt-out: never touch the keychain; the sink daemon won't
	// read Chrome Safe Storage. This is also the pre-v0.13 friction #19
	// fix (no 60s strategy-loop timeout in degraded mode).
	if skipChromeSQLite {
		fmt.Fprintln(os.Stderr, "agentcookie wizard: skipping set-keychain-access strategy loop (degraded mode: sidecar+adapter delivery does not need Chrome Safe Storage access)")
		return skipChromeSQLite, cdpEnabled, cdpProfileDir, delivery, false
	}

	// Universal config (skip=false). The keychain prompt / loop is the
	// explicit opt-out path: --skip-keychain-prompt and
	// --skip-keychain-access leave the keychain untouched. The operator
	// asked for universal config without the open, so render universal
	// and do not attempt the open.
	if wizardSkipKeychainAccess || wizardSkipKeychainPrompt {
		fmt.Fprintln(os.Stderr, "agentcookie wizard: keychain open skipped by flag; rendering universal config without opening Chrome Safe Storage (cookie CLIs may prompt until you grant access once)")
		return skipChromeSQLite, cdpEnabled, cdpProfileDir, delivery, false
	}

	// Universal default / forced-universal: attempt the inline one-password
	// partition open. Its outcome determines the final delivery mode for the
	// default case.
	fmt.Fprintln(os.Stderr, "agentcookie wizard: opening Chrome Safe Storage via the one-password partition path (universal delivery: any cookie CLI reads it; you'll be asked for your macOS login password once, no GUI prompt)")
	if err := attemptUniversalKeychainOpen(); err != nil {
		if wizardWriteChromeSQLite {
			// EXPLICIT --write-chrome-sqlite: honor the intent. Surface a
			// clear warning but do NOT downgrade; the operator forced
			// universal and we respect that even if the box cannot open
			// the key right now.
			fmt.Fprintf(os.Stderr, "agentcookie wizard: WARNING --write-chrome-sqlite forced universal but the keychain open did not complete: %v\n", err)
			fmt.Fprintln(os.Stderr, "agentcookie wizard:   honoring explicit --write-chrome-sqlite; the sink will write the real Default profile but may fail to read Chrome Safe Storage until you open it once.")
			fmt.Fprintln(os.Stderr, "agentcookie wizard:   open it over SSH with one password: agentcookie wizard set-keychain-access   (or non-interactively: AGENTCOOKIE_LOGIN_PASSWORD=… agentcookie wizard set-keychain-access)")
			return skipChromeSQLite, cdpEnabled, cdpProfileDir, delivery, false
		}
		// DEFAULT (unspecified) case: DOWNGRADE to degraded, non-fatally.
		// The open could not complete (most often: fully non-interactive
		// install with no AGENTCOOKIE_LOGIN_PASSWORD set). Rendering universal
		// here would leave a sink daemon that cannot read the key and so
		// cannot start. Fall back to degraded (skip=true + CDP) so the
		// install completes with a working sink.
		fmt.Fprintf(os.Stderr, "agentcookie wizard: WARNING universal keychain open did not complete: %v\n", err)
		fmt.Fprintln(os.Stderr, "agentcookie wizard:   downgrading this install to degraded (sidecar+adapter delivery + CDP); the sink is installed and syncing.")
		fmt.Fprintln(os.Stderr, "agentcookie wizard:   upgrade to universal over SSH with one password: agentcookie wizard set-keychain-access   (non-interactive: AGENTCOOKIE_LOGIN_PASSWORD=… agentcookie wizard set-keychain-access)")
		degradedSkip := true
		degradedCDP := false
		degradedProfileDir := ""
		if !wizardNoCDP {
			degradedCDP = true
			degradedProfileDir = "~/.agentcookie/chrome-profile"
		}
		return degradedSkip, degradedCDP, degradedProfileDir, deliveryDegraded, false
	}
	// Open succeeded: final config is universal.
	return skipChromeSQLite, cdpEnabled, cdpProfileDir, delivery, true
}

// expandHome resolves a leading ~/ in a path against the current user's
// home directory. Cleaner than relying on the shell to expand at YAML
// load time, which it does not.
func expandHome(p string) string {
	if p == "" || p[0] != '~' {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, p[1:])
}

// validateListenAddr enforces the v0.12 binding policy on a configured
// listen address: must be tailnet (100.x) or an explicit local-dev
// loopback. 0.0.0.0 and other any-interface binds are rejected. Used
// by the wizard when --listen is passed explicitly, and by the sink
// and pair runtime startup guards.
//
// The address string is in "host:port" form. Anything that does not
// parse cleanly is rejected with a wrapping error so the caller can
// surface "what you passed" plus the policy explanation.
func validateListenAddr(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("parse host:port: %w (v0.12 requires a tailnet 100.x address; see docs/quickstart.md)", err)
	}
	switch host {
	case "0.0.0.0", "::", "":
		return fmt.Errorf("refuses to bind on %q (every interface); v0.12 requires a tailnet 100.x address (run `tailscale status` and re-run; see docs/quickstart.md)", host)
	case "127.0.0.1", "::1", "localhost":
		// Explicit loopback is allowed for local-dev / test setups.
		// Not the default fallback path; the operator typed it.
		return nil
	}
	if tsclient.IsTailnetIP(host) {
		return nil
	}
	return fmt.Errorf("refuses to bind on %q: not a Tailscale 100.x address (v0.12 hardening; pin a 100.x IP from `tailscale status` or use 127.0.0.1 for local dev)", host)
}

func starterBlocklistYAML() string {
	return `# blocklist.yaml: domains to KEEP on this machine (NOT synced to the peer).
# Empty file = sync everything. Prefer agentcookie accounts off <domain>
# for normal site toggles; it writes exact + subdomain-safe patterns.
version: 1
domains:
  # Banking / brokerage / personal finance:
  # - pattern: "chase.com"
  # - pattern: "%.chase.com"
  # - pattern: "vanguard.com"
  # - pattern: "%.vanguard.com"
  # - pattern: "fidelity.com"
  # - pattern: "%.fidelity.com"
  # - pattern: "schwab.com"
  # - pattern: "%.schwab.com"
  # - pattern: "bankofamerica.com"
  # - pattern: "%.bankofamerica.com"
  # Password managers (probably never want these on a second machine):
  # - pattern: "1password.com"
  # - pattern: "%.1password.com"
  # - pattern: "bitwarden.com"
  # - pattern: "%.bitwarden.com"
  # - pattern: "lastpass.com"
  # - pattern: "%.lastpass.com"
  # Tax / IRS:
  # - pattern: "irs.gov"
  # - pattern: "%.irs.gov"
  # - pattern: "turbotax.intuit.com"
  # - pattern: "%.turbotax.intuit.com"
  # Health / insurance:
  # - pattern: "kaiserpermanente.org"
  # - pattern: "%.kaiserpermanente.org"
  # - pattern: "bcbs.com"
  # - pattern: "%.bcbs.com"
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
