package cli

import (
	"strings"
	"testing"

	"github.com/mvanhorn/agentcookie/internal/chromebackup"
)

func TestIncompleteForIgnoresDataTheRestoreLeavesAlone(t *testing.T) {
	safety := chromebackup.Snapshot{Manifest: chromebackup.Manifest{
		ExtraDirs: []string{"IndexedDB/site"},
		Profiles: []chromebackup.Profile{{Dir: "Profile 1", Errors: []string{
			"IndexedDB/site: /x/IndexedDB/site/link is a symlink; not copied",
			"History: disk full",
			"mkdir /x/Profile 1: permission denied",
		}}},
	}}
	light := chromebackup.Snapshot{Manifest: chromebackup.Manifest{Profiles: []chromebackup.Profile{
		{Dir: "Profile 1", Files: []string{"Cookies", "Preferences"}},
	}}}
	got := incompleteFor(safety, light, nil)
	if len(got) != 1 || !strings.Contains(got[0], "mkdir") {
		t.Fatalf("light restore blocked by %v", got)
	}
	full := chromebackup.Snapshot{Manifest: chromebackup.Manifest{Profiles: []chromebackup.Profile{
		{Dir: "Profile 1", Files: []string{"Cookies", "History", "IndexedDB/"}},
	}}}
	if got := incompleteFor(safety, full, nil); len(got) != 3 {
		t.Fatalf("full restore errors = %v", got)
	}
	if got := incompleteFor(safety, full, []string{"Default"}); len(got) != 0 {
		t.Fatalf("other profile errors = %v", got)
	}
}

func TestUncoveredFolders(t *testing.T) {
	pick := chromebackup.Snapshot{Manifest: chromebackup.Manifest{Profiles: []chromebackup.Profile{
		{Dir: "Profile 1", Files: []string{"Cookies", "Local Extension Settings/", "IndexedDB/"}},
	}}}
	safety := chromebackup.Snapshot{Manifest: chromebackup.Manifest{ExtraDirs: []string{"IndexedDB/site"}}}
	if got := uncoveredFolders(pick, safety, nil); len(got) != 1 || !strings.Contains(got[0], "IndexedDB") {
		t.Fatalf("uncovered = %v", got)
	}
	safety.ExtraDirs = []string{"IndexedDB"}
	if got := uncoveredFolders(pick, safety, nil); len(got) != 0 {
		t.Fatalf("uncovered = %v", got)
	}
}
