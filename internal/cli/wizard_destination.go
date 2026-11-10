package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/keystore"
	"github.com/mvanhorn/agentcookie/internal/launchd"
	"github.com/mvanhorn/agentcookie/internal/onboarding"
	"github.com/mvanhorn/agentcookie/internal/pairing"
	"github.com/mvanhorn/agentcookie/internal/tsclient"
)

func wizardInstallLiveSink(ctx context.Context, binPath, logDir string) error {
	kind := selectedWizardDestination()
	if !wizardDryRun && (wizardCode == "" || wizardPairURL == "") {
		keyPath, _ := keystore.Path(common.ConfigDir, wizardPeer)
		if !fileExists(keyPath) || wizardRepair {
			return fmt.Errorf("--code and --pair-url are required for a new sink pairing")
		}
	}
	if kind != "live_chrome" && kind != "attach" {
		return fmt.Errorf("destination must be live_chrome, attach or ppcli")
	}
	path := filepath.Join(common.ConfigDir, "sink.yaml")
	if err := guardConfigPeerMismatch("sink", path, wizardPeer); err != nil {
		return err
	}
	cfg := &config.SinkConfig{Peer: config.PeerRef{Hostname: wizardPeer}, SkipChromeSQLite: true}
	if fileExists(path) && !wizardForce {
		existing, err := config.LoadSink(common.ConfigDir)
		if err != nil {
			return err
		}
		if !existing.HasDestinations() && !existing.LiveCDP.Enabled {
			return fmt.Errorf("existing sink is legacy; review and pass --force to migrate to %s", kind)
		}
		current, err := existing.ResolveDestinations()
		if err != nil {
			return err
		}
		if len(current) != 1 || current[0].Kind != kind {
			return fmt.Errorf("existing sink destination differs; review and pass --force to migrate to %s", kind)
		}
		cfg = existing
	} else {
		headed := true
		cfg.Destination = &config.DestinationRef{Name: kind, Kind: kind, Required: true, Endpoint: wizardDestinationEndpoint, Chrome: config.DestinationChrome{UserDataDir: wizardDestinationProfile, Port: wizardDestinationPort, Headed: &headed, Path: wizardChromePath, Start: "on_boot", Relaunch: "after_crash"}}
		if kind == "live_chrome" {
			cfg.Destination.Endpoint = ""
		}
		ds, err := cfg.ResolveDestinations()
		if err != nil {
			return err
		}
		cfg.Destination = &ds[0]
		if wizardDryRun {
			cfg.Listen.Addr = "REPLACE_WITH_SINK_TAILSCALE_IP:9999"
		} else {
			ip, err := tsclient.RequireTailnetIP(ctx)
			if err != nil {
				return fmt.Errorf("detect Tailscale sink address: %w", err)
			}
			cfg.Listen.Addr = ip + ":9999"
		}
	}
	ds, err := cfg.ResolveDestinations()
	if err != nil {
		return err
	}
	if len(ds) != 1 || ds[0].Kind == "ppcli" {
		return fmt.Errorf("wizard handles one live destination; edit multi-destination config explicitly")
	}
	d := ds[0]
	fmt.Fprintf(os.Stderr, "Effective destination: source %s -> %s (%s); owned profile=%s CDP port=%d endpoint=%s\n", wizardPeer, d.Name, d.Kind, d.Chrome.UserDataDir, d.Chrome.Port, d.Endpoint)
	fmt.Fprintln(os.Stderr, "The CDP endpoint permits local session access. Readiness is verified before daemon installation; the app opens a window only through the running sink after restore.")
	body, err := renderDestinationSinkYAML(cfg, d)
	if err != nil {
		return err
	}
	if wizardDryRun {
		fmt.Print(body)
		return nil
	}
	// No visible window during preflight. Stop the preflight-owned Chrome before
	// handing lifecycle ownership to the daemon, so there is no duplicate owner.
	if err = wizardDestinationPreflight(ctx, cfg); err != nil {
		return fmt.Errorf("destination readiness check failed; daemon not installed: %w", err)
	}
	if err = os.MkdirAll(common.ConfigDir, 0700); err != nil {
		return err
	}
	if err = writeYAMLIfMissing(path, body, wizardForce); err != nil {
		return err
	}
	if err = writeYAMLIfMissing(filepath.Join(common.ConfigDir, "blocklist.yaml"), starterBlocklistYAML(), wizardForce); err != nil {
		return err
	}
	keyPath, _ := keystore.Path(common.ConfigDir, wizardPeer)
	if !fileExists(keyPath) || wizardRepair {
		if wizardCode == "" || wizardPairURL == "" {
			return fmt.Errorf("--code and --pair-url are required for a new sink pairing")
		}
		res, err := pairing.RunSink(ctx, wizardPairURL, pairing.Code(wizardCode), wizardLocalName)
		if err != nil {
			return fmt.Errorf("sink pairing: %w", err)
		}
		if err = keystore.Save(common.ConfigDir, &keystore.PeerKey{Peer: wizardPeer, Key: res.Key, PairedAt: res.PairedAt, Fingerprint: res.Fingerprint, ProtocolVer: pairing.ProtocolVersion}); err != nil {
			return err
		}
	}
	if runtime.GOOS == "darwin" && d.Kind == "live_chrome" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		app := filepath.Join(home, "Applications", "agentcookie Chrome.app")
		dir, err := filepath.Abs(common.ConfigDir)
		if err != nil {
			return err
		}
		if _, err = os.Lstat(app); os.IsNotExist(err) {
			if err = onboarding.InstallChromeApp(app, binPath, dir, d.Name); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else {
			expected, e := onboarding.ChromeLauncher(binPath, dir, d.Name)
			if e != nil {
				return e
			}
			actual, e := os.ReadFile(filepath.Join(app, "Contents", "MacOS", "launch"))
			if e != nil || string(actual) != expected {
				return fmt.Errorf("existing %s does not match this sink; preserve or rename it, then use chrome-launcher install --destination %s --output <new-app-path>", app, d.Name)
			}
		}
	}
	if !wizardSkipDaemon {
		if config.IsLinux() {
			printLinuxDaemonInstructions("sink", binPath, logDir, nil)
		} else if err = installLaunchAgent(launchd.Spec{Role: launchd.RoleSink, BinaryPath: binPath, LogDir: logDir}); err != nil {
			return err
		}
	}
	fmt.Fprintln(os.Stderr, "Destination preflight ready. Run doctor and status after the first sync; accepted is not applied. Open agentcookie Chrome.app only after the sink is running.")
	return nil
}
