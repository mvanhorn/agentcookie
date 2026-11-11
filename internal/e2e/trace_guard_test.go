package e2e

import (
	"strings"
	"testing"
)

// Gecko may list storage directories, but source/ broker processes must not
// open live cookie or storage database files. Firefox itself is not traced.
func forbiddenGeckoDatabaseTrace(line, profile string) bool {
	// Directory inventory may stat file names; only content access/open actions
	// violate this guard. fs_usage formats the syscall immediately after time.
	access := false
	fields := strings.Fields(line)
	for _, field := range fields[:min(3, len(fields))] {
		action, _, _ := strings.Cut(field, "(")
		switch action {
		case "open", "openat", "openat2", "open_nocancel", "read", "pread", "read_nocancel", "RdData", "mmap", "PageIn":
			access = true
		}
	}
	if !access {
		return false
	}
	aliases := []string{profile, strings.TrimPrefix(profile, "/private")}
	for _, root := range aliases {
		at := strings.Index(line, root+"/")
		if at < 0 {
			continue
		}
		path := strings.ToLower(line[at+len(root):])
		if strings.Contains(path, "/cookies.sqlite") {
			return true
		}
		if strings.Contains(path, "/storage/") && (strings.Contains(path, ".sqlite") || strings.Contains(path, ".ldb") || strings.Contains(path, ".sst") || strings.Contains(path, ".log")) {
			return true
		}
	}
	return false
}
func TestGeckoTraceGuardClassifiesDatabaseAccess(t *testing.T) {
	profile := "/private/tmp/acg-fixture/gecko-profile"
	for _, suffix := range []string{"/cookies.sqlite", "/cookies.sqlite-wal", "/storage/default/http+++localhost/ls/data.sqlite", "/storage/default/http+++localhost/idb/123.sqlite", "/storage/default/http+++localhost/idb/00001.ldb"} {
		if !forbiddenGeckoDatabaseTrace("open "+profile+suffix+" reader.123", profile) {
			t.Fatal(suffix)
		}
		if !forbiddenGeckoDatabaseTrace("open "+strings.TrimPrefix(profile, "/private")+suffix, profile) {
			t.Fatal("alias", suffix)
		}
	}
	for _, line := range []string{"open " + profile + "/storage/default", "getattrlist " + profile + "/storage/default/http+++localhost/idb", "open /tmp/other/cookies.sqlite", "open " + profile + "/WebDriverBiDiServer.json", "getattrlist " + profile + "/storage/default/http+++localhost/idb/123.sqlite"} {
		if forbiddenGeckoDatabaseTrace(line, profile) {
			t.Fatal(line)
		}
	}
}
