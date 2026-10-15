package cli

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/mvanhorn/agentcookie/internal/secretsbus"
)

// envSkipLocalStorage lets a LaunchAgent turn off Local Storage packing
// without a flag edit, like AGENTCOOKIE_SKIP_DBSC_SUSPECT.
const envSkipLocalStorage = "AGENTCOOKIE_SKIP_LOCAL_STORAGE"

// skipLocalStorage reports whether pushes should leave out the browser's
// Local Storage leveldb. It is read per push, like AGENTCOOKIE_SYNC_INDEXEDDB.
func skipLocalStorage() bool {
	return sourceSkipLocalStorage || os.Getenv(envSkipLocalStorage) == "1"
}

// envSkipSecrets lets a LaunchAgent turn off the secrets bus without a flag
// edit. Unlike its siblings it fails closed: any value other than empty or
// "0" turns it on, so a typo such as "true" cannot keep shipping secrets.
const envSkipSecrets = "AGENTCOOKIE_SKIP_SECRETS"

var (
	skipSecretsWarnMu    sync.Mutex
	skipSecretsWarnedFor string
)

// skipSecrets reports whether pushes should leave out the secrets bus
// entirely. It is read per push. A value other than "1" still turns it on
// and prints one stderr warning per distinct value.
func skipSecrets() bool {
	v := os.Getenv(envSkipSecrets)
	envOn := v != "" && v != "0"
	if envOn && v != "1" {
		skipSecretsWarnMu.Lock()
		if skipSecretsWarnedFor != v {
			skipSecretsWarnedFor = v
			fmt.Fprintf(os.Stderr, "agentcookie source: %s=%q is not 1 or 0; treating it as on and skipping the secrets bus\n", envSkipSecrets, v)
		}
		skipSecretsWarnMu.Unlock()
	}
	return sourceSkipSecrets || envOn
}

// secretsSummary is what one push did with the secrets bus, recorded in
// source state for `agentcookie doctor`.
type secretsSummary struct {
	skipped bool
	clis    int
}

// secretsBusRunner is one secrets-bus watcher that source --watch runs.
type secretsBusRunner struct {
	name string
	run  func(context.Context) error
}

// secretsBusWatchers builds the watchers that push on secrets-bus and
// discovery changes. With skip on it builds none: discovery reads
// manifests, which --skip-secrets forbids.
func secretsBusWatchers(skip bool, home string, verbose bool, fire func(context.Context)) []secretsBusRunner {
	if skip {
		return nil
	}
	// v0.13: watch ~/.agentcookie/secrets/ so a write to a per-CLI
	// secrets.env triggers the same push pipeline as a Chrome cookie
	// change. The secrets watcher tolerates a missing root (waits for the
	// friend to create it).
	secretsWatcher := secretsbus.NewWatcher(home, 0, fire)
	// v0.14: also watch the v2 discovery paths (~/.agentcookie/manifests/
	// + PP library) so dropping a new agentcookie.toml or regenerating a
	// PP CLI triggers a push without restart.
	discoveryWatcher := secretsbus.NewDiscoveryWatcher(
		secretsbus.DiscoveryConfig{HomeDir: home},
		0,
		func(ctx context.Context, delta secretsbus.RegistryDelta, _ *secretsbus.Registry) {
			if verbose && (len(delta.Added)+len(delta.Removed) > 0) {
				fmt.Fprintf(os.Stderr, "agentcookie source --watch: discovery: added=%v removed=%v\n", delta.Added, delta.Removed)
			}
			fire(ctx)
		},
	)
	return []secretsBusRunner{
		{name: "secrets-bus watcher", run: secretsWatcher.Run},
		{name: "discovery watcher", run: discoveryWatcher.Run},
	}
}
