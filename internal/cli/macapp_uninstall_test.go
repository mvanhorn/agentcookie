package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mvanhorn/agentcookie/internal/macapp"
)

func TestRemoveAppIfUnused(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	agents := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(filepath.Join(macapp.Path(home), "Contents", "MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(agents, 0o755); err != nil {
		t.Fatal(err)
	}
	cmux := filepath.Join(agents, "dev.agentcookie.cmux-sync.plist")
	if err := os.WriteFile(cmux, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A leftover one-shot plist does not keep the app.
	if err := os.WriteFile(filepath.Join(agents, "dev.agentcookie.oneshot.1a2b3c4d.plist"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	removeAppIfUnused()
	if _, err := os.Stat(macapp.Path(home)); err != nil {
		t.Fatalf("app removed while cmux-sync plist remains: %v", err)
	}

	if err := os.Remove(cmux); err != nil {
		t.Fatal(err)
	}
	removeAppIfUnused()
	if _, err := os.Stat(macapp.Path(home)); !os.IsNotExist(err) {
		t.Fatalf("app still present after the last agentcookie plist went: %v", err)
	}
}
