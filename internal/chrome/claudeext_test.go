package chrome

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/syndtr/goleveldb/leveldb"
)

// writeClaudeExtFixture builds <root>/<profile>/Local Extension Settings/<id>
// as a real LevelDB. An empty account writes the store with no accountUuid,
// which is what a signed-out extension looks like.
func writeClaudeExtFixture(t *testing.T, root, profile, account string) {
	t.Helper()
	dir := filepath.Join(root, profile, "Local Extension Settings", ClaudeExtensionID)
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := leveldb.OpenFile(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Put([]byte("anonymousId"), []byte(`"anon"`), nil); err != nil {
		t.Fatal(err)
	}
	if account != "" {
		v, _ := json.Marshal(account)
		if err := db.Put([]byte("accountUuid"), v, nil); err != nil {
			t.Fatal(err)
		}
	}
}

func profileNames(ps []ClaudeProfile) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Profile)
	}
	sort.Strings(out)
	return out
}

func leftoverTempDirs(t *testing.T, tmp string) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(tmp, claudeExtTempPrefix+"*"))
	return m
}

func useTempDir(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	return tmp
}

func TestQualifyingClaudeProfiles_DistinctAccountsQualify(t *testing.T) {
	useTempDir(t)
	root := t.TempDir()
	writeClaudeExtFixture(t, root, "Default", "acct-a")
	writeClaudeExtFixture(t, root, "Profile 1", "acct-b")

	got, _ := QualifyingClaudeProfiles(root)
	if strings.Join(profileNames(got), ",") != "Default,Profile 1" {
		t.Fatalf("got %v, want Default and Profile 1", profileNames(got))
	}
	for _, p := range got {
		want := map[string]string{"Default": "acct-a", "Profile 1": "acct-b"}[p.Profile]
		if p.AccountUUID != want {
			t.Errorf("%s account = %q, want %q", p.Profile, p.AccountUUID, want)
		}
	}
}

func TestQualifyingClaudeProfiles_SameAccountQualifiesNone(t *testing.T) {
	useTempDir(t)
	root := t.TempDir()
	writeClaudeExtFixture(t, root, "Default", "acct-a")
	writeClaudeExtFixture(t, root, "Profile 1", "acct-a")

	if got, _ := QualifyingClaudeProfiles(root); len(got) != 0 {
		t.Fatalf("same account must not qualify, got %v", profileNames(got))
	}
}

func TestQualifyingClaudeProfiles_OneProfileWithoutExtension(t *testing.T) {
	useTempDir(t)
	root := t.TempDir()
	writeClaudeExtFixture(t, root, "Default", "acct-a")
	if err := os.MkdirAll(filepath.Join(root, "Profile 1"), 0o700); err != nil {
		t.Fatal(err)
	}

	if got, _ := QualifyingClaudeProfiles(root); len(got) != 0 {
		t.Fatalf("one extension profile must not qualify, got %v", profileNames(got))
	}
}

func TestQualifyingClaudeProfiles_SignedOutProfileExcluded(t *testing.T) {
	useTempDir(t)
	root := t.TempDir()
	writeClaudeExtFixture(t, root, "Default", "acct-a")
	writeClaudeExtFixture(t, root, "Profile 1", "acct-b")
	writeClaudeExtFixture(t, root, "Profile 2", "")

	got, errs := QualifyingClaudeProfiles(root)
	if strings.Join(profileNames(got), ",") != "Default,Profile 1" {
		t.Fatalf("got %v, want signed-out Profile 2 excluded", profileNames(got))
	}
	if len(errs) != 0 {
		t.Fatalf("signed-out is not an error, got %v", errs)
	}
}

func TestQualifyingClaudeProfiles_CorruptStoreExcludedWithError(t *testing.T) {
	useTempDir(t)
	root := t.TempDir()
	writeClaudeExtFixture(t, root, "Default", "acct-a")
	writeClaudeExtFixture(t, root, "Profile 1", "acct-b")
	bad := filepath.Join(root, "Profile 2", "Local Extension Settings", ClaudeExtensionID)
	if err := os.MkdirAll(bad, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, "CURRENT"), []byte("garbage\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, errs := QualifyingClaudeProfiles(root)
	if strings.Join(profileNames(got), ",") != "Default,Profile 1" {
		t.Fatalf("got %v, want corrupt Profile 2 excluded", profileNames(got))
	}
	if len(errs) != 1 {
		t.Fatalf("want one error for the corrupt store, got %v", errs)
	}
}

func TestQualifyingClaudeProfiles_SharedAccountStillQualifiesAll(t *testing.T) {
	useTempDir(t)
	root := t.TempDir()
	writeClaudeExtFixture(t, root, "Default", "acct-a")
	writeClaudeExtFixture(t, root, "Profile 1", "acct-b")
	writeClaudeExtFixture(t, root, "Profile 2", "acct-a")

	got, _ := QualifyingClaudeProfiles(root)
	if strings.Join(profileNames(got), ",") != "Default,Profile 1,Profile 2" {
		t.Fatalf("got %v, want all three", profileNames(got))
	}
}

func TestQualifyingClaudeProfiles_LeavesNoTempCopies(t *testing.T) {
	tmp := useTempDir(t)
	root := t.TempDir()
	writeClaudeExtFixture(t, root, "Default", "acct-a")
	writeClaudeExtFixture(t, root, "Profile 1", "acct-b")
	bad := filepath.Join(root, "Profile 2", "Local Extension Settings", ClaudeExtensionID)
	_ = os.MkdirAll(bad, 0o700)
	_ = os.WriteFile(filepath.Join(bad, "CURRENT"), []byte("garbage\n"), 0o600)

	QualifyingClaudeProfiles(root)
	if left := leftoverTempDirs(t, tmp); len(left) != 0 {
		t.Fatalf("temp copies left behind: %v", left)
	}
}

func TestCleanupClaudeExtTemp_RemovesLeftovers(t *testing.T) {
	tmp := useTempDir(t)
	stale := filepath.Join(tmp, claudeExtTempPrefix+"stale")
	if err := os.MkdirAll(filepath.Join(stale, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(tmp, "unrelated")
	_ = os.MkdirAll(keep, 0o700)

	CleanupClaudeExtTemp()
	if left := leftoverTempDirs(t, tmp); len(left) != 0 {
		t.Fatalf("leftover not removed: %v", left)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("unrelated dir removed: %v", err)
	}
}
