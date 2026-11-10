package destination

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOwnedProfileIdentityPersistsButNeverSurvivesReplacement(t *testing.T) {
	dir := t.TempDir()
	first, err := ownedProfileIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	again, err := ownedProfileIdentity(dir)
	if err != nil || first != again {
		t.Fatalf("identity not stable: %v", err)
	}
	path := filepath.Join(dir, ".agentcookie-profile-id")
	marker, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	copyDir := t.TempDir()
	if err = os.WriteFile(filepath.Join(copyDir, ".agentcookie-profile-id"), marker, 0600); err != nil {
		t.Fatal(err)
	}
	copied, err := ownedProfileIdentity(copyDir)
	if err != nil || copied == first {
		t.Fatal("copied profile inherited identity")
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	replaced, err := ownedProfileIdentity(dir)
	if err != nil || replaced == first {
		t.Fatal("replacement profile inherited identity")
	}
}

func TestOwnedProfileIdentityRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "marker")
	if err := os.WriteFile(target, []byte("01234567890123456789012345678901"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, ".agentcookie-profile-id")); err != nil {
		t.Fatal(err)
	}
	if _, err := ownedProfileIdentity(dir); err == nil {
		t.Fatal("symlink marker trusted")
	}
}
