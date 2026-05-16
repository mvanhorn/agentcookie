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

	"github.com/mvanhorn/agentcookie/internal/keystore"
	"github.com/mvanhorn/agentcookie/internal/launchd"
	"github.com/mvanhorn/agentcookie/internal/pairing"
)

// Wizard flags. These are read by both `install` and `uninstall`.
var (
	wizardRole       string
	wizardPeer       string
	wizardListen     string
	wizardLocalName  string
	wizardSinkURL    string
	wizardCode       string
	wizardPairURL    string
	wizardRepair     bool
	wizardForce      bool
	wizardSkipDaemon bool
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

	fmt.Fprintln(os.Stderr, "agentcookie wizard: sink install complete")
	return nil
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
	return fmt.Sprintf(`listen:
  addr: %s
cdp:
  enabled: true
  managed: true
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
