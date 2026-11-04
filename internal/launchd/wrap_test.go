package launchd

import (
	"os"
	"path/filepath"
	"testing"
)

const testLauncher = "/h/Applications/agentcookie.app/Contents/MacOS/agentcookie-launcher"

func TestLauncherFor(t *testing.T) {
	accept := func(string) string { return testLauncher }
	reject := func(string) string { return "" }

	cases := []struct {
		name    string
		role    Role
		marker  bool
		wrapper func(string) string
		want    string
	}{
		{"source wraps", RoleSource, false, accept, testLauncher},
		{"cmux-sync wraps", RoleCmuxSync, false, accept, testLauncher},
		{"sink stays bare", RoleSink, false, accept, ""},
		{"revert marker keeps bare", RoleSource, true, accept, ""},
		{"no valid app or target rejected", RoleSource, false, reject, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			if c.marker {
				if err := os.MkdirAll(filepath.Dir(RevertMarker(home)), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(RevertMarker(home), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			called := false
			w := Wrapping{Home: home, Wrapper: func(target string) string {
				called = true
				if target != "/h/bin/agentcookie" {
					t.Errorf("wrapper got target %q", target)
				}
				return c.wrapper(target)
			}}
			if got := LauncherFor(w, c.role, "/h/bin/agentcookie"); got != c.want {
				t.Errorf("LauncherFor = %q, want %q", got, c.want)
			}
			if (c.role == RoleSink || c.marker) && called {
				t.Error("wrapper ran (and could install the app) for a job that must stay bare")
			}
		})
	}
}

func TestRevertMarkerPath(t *testing.T) {
	if got, want := RevertMarker("/h"), "/h/.agentcookie/services-unwrapped"; got != want {
		t.Errorf("RevertMarker = %q, want %q", got, want)
	}
}
