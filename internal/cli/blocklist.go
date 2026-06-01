package cli

import "github.com/mvanhorn/agentcookie/internal/config"

// loadFreshBlocklist loads the current blocklist for one sync boundary.
// A missing blocklist.yaml preserves v0.3 sync-all semantics; any returned
// error must be treated by callers as fail-closed, not as an empty blocklist.
func loadFreshBlocklist() (*config.Blocklist, error) {
	return config.LoadBlocklist(common.ConfigDir)
}
