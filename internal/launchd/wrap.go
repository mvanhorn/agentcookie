package launchd

import (
	"os"
	"path/filepath"

	"github.com/mvanhorn/agentcookie/internal/macapp"
)

// RevertMarkerName is the file under ~/.agentcookie that `agentcookie
// services refresh --revert` writes. While it exists, every plist write
// leaves the job bare instead of starting it through agentcookie.app.
const RevertMarkerName = "services-unwrapped"

// RevertMarker returns the marker's path for home.
func RevertMarker(home string) string {
	return filepath.Join(home, ".agentcookie", RevertMarkerName)
}

// Reverted reports whether home's revert marker exists.
func Reverted(home string) bool {
	_, err := os.Lstat(RevertMarker(home))
	return err == nil
}

// Wrapping configures LauncherFor. Zero values mean the running system.
type Wrapping struct {
	Home string
	// Wrapper returns the launcher to start target through, or "". It
	// defaults to macapp.Wrapper, which also installs or updates the app.
	Wrapper func(target string) string
}

// LauncherFor is the one wrap decision every plist write uses. It returns
// the agentcookie.app launcher a job for role running target should start
// through, or "" to run target directly. A job is wrapped only when the
// revert marker is absent, the role is not the sink, a valid app is
// installed and target passes the launcher's signature requirement.
//
// The sink stays bare: it quits Chrome through osascript, and macOS may
// credit that Automation grant to the app instead of agentcookie, which
// would quietly stop the quit before the cookie write.
func LauncherFor(w Wrapping, role Role, target string) string {
	if role == RoleSink || target == "" {
		return ""
	}
	if w.Home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		w.Home = h
	}
	if Reverted(w.Home) {
		return ""
	}
	if w.Wrapper == nil {
		home := w.Home
		w.Wrapper = func(t string) string { return macapp.Wrapper(macapp.Options{Home: home}, t) }
	}
	return w.Wrapper(target)
}
