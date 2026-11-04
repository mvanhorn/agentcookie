package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mvanhorn/agentcookie/internal/launchd"
	"github.com/mvanhorn/agentcookie/internal/macapp"
)

// managedRoles are the LaunchAgents agentcookie installs, and the only ones
// that can start through agentcookie.app.
var managedRoles = []launchd.Role{launchd.RoleSource, launchd.RoleSink, launchd.RoleCmuxSync}

// removeAppIfUnused deletes ~/Applications/agentcookie.app once no
// agentcookie LaunchAgent plist is left to start through it. Best-effort:
// uninstall has already succeeded by the time this runs.
func removeAppIfUnused() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	if appInUse(home) {
		return
	}
	if _, err := os.Stat(macapp.Path(home)); err != nil {
		return
	}
	if err := macapp.Remove(home); err != nil {
		fmt.Fprintf(os.Stderr, "agentcookie: could not remove %s: %v\n", macapp.Path(home), err)
		return
	}
	fmt.Fprintf(os.Stderr, "agentcookie: removed %s (no agentcookie LaunchAgents left)\n", macapp.Path(home))
}

// appInUse reports whether any agentcookie LaunchAgent plist remains in
// home's ~/Library/LaunchAgents.
func appInUse(home string) bool {
	for _, r := range managedRoles {
		p := filepath.Join(home, "Library", "LaunchAgents", launchd.Spec{Role: r}.Label()+".plist")
		if _, err := os.Lstat(p); err == nil {
			return true
		}
	}
	return false
}
