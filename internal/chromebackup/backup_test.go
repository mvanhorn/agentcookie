package chromebackup

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// makeProfile builds a fake profile folder with n cookies and the given
// Web Store extensions.
func makeProfile(t *testing.T, root, dir string, cookies int, exts ...string) {
	t.Helper()
	p := filepath.Join(root, dir)
	if err := os.MkdirAll(filepath.Join(p, "Local Extension Settings", "abc"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeCookies(t, filepath.Join(p, "Cookies"), cookies)
	settings := map[string]any{}
	for _, id := range exts {
		settings[id] = map[string]any{"location": 1, "state": 1, "manifest": map[string]any{"name": "ext " + id}}
	}
	settings["component"] = map[string]any{"location": 5, "manifest": map[string]any{"name": "built in"}}
	writeJSON(t, filepath.Join(p, "Preferences"), map[string]any{"extensions": map[string]any{"settings": settings}})
	writeJSON(t, filepath.Join(p, "Secure Preferences"), map[string]any{})
	if err := os.WriteFile(filepath.Join(p, "Bookmarks"), []byte(`{"roots":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "Local Extension Settings", "abc", "000003.log"), []byte("pairing"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "Local Extension Settings", "abc", "LOCK"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeCookies(t *testing.T, path string, n int) {
	t.Helper()
	_ = os.Remove(path)
	_ = os.Remove(path + "-wal")
	db, err := sql.Open("sqlite3", "file:"+path+"?_journal=WAL")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA wal_autocheckpoint=0; CREATE TABLE cookies (host_key TEXT, name TEXT)`); err != nil {
		t.Fatal(err)
	}
	for i := range n {
		if _, err := db.Exec(`INSERT INTO cookies VALUES (?, ?)`, fmt.Sprintf("h%d.example", i), "c"); err != nil {
			t.Fatal(err)
		}
	}
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func newRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeJSON(t, filepath.Join(root, "Local State"), map[string]any{"profile": map[string]any{"info_cache": map[string]any{
		"Default": map[string]any{"name": "Work"}, "Profile 1": map[string]any{"name": "Main"},
	}}})
	makeProfile(t, root, "Default", 300, "aaa", "bbb", "ccc")
	makeProfile(t, root, "Profile 1", 250, "ddd", "eee", "fff", "ggg")
	// Not profiles: skipped.
	_ = os.MkdirAll(filepath.Join(root, "System Profile"), 0o700)
	_ = os.MkdirAll(filepath.Join(root, "Profile 9"), 0o700) // no Preferences
	return root
}

var t0 = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

func TestTakeRecordsCountsAndFiles(t *testing.T) {
	root, dir := newRoot(t), t.TempDir()
	s, err := Take(root, dir, t0, true, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Profiles) != 2 {
		t.Fatalf("profiles = %+v", s.Profiles)
	}
	d := s.Profiles[0]
	if d.Dir != "Default" || d.Name != "Work" || d.Cookies != 300 || len(d.Extensions) != 3 || len(d.Errors) != 0 {
		t.Fatalf("Default = %+v", d)
	}
	if d.Extensions[0].ID != "aaa" || d.Extensions[0].Name != "ext aaa" || !d.Extensions[0].Enabled {
		t.Fatalf("extension = %+v", d.Extensions[0])
	}
	want := "Cookies,Preferences,Secure Preferences,Bookmarks,Local Extension Settings/"
	if got := strings.Join(d.Files, ","); got != want {
		t.Fatalf("files = %s, want %s", got, want)
	}
	if _, err := os.Stat(filepath.Join(s.Path, "Default", "Local Extension Settings", "abc", "LOCK.gz")); !os.IsNotExist(err) {
		t.Fatalf("LOCK was copied: %v", err)
	}
	loaded, err := List(dir)
	if err != nil || len(loaded) != 1 || loaded[0].ID != "20261117T120000Z" || !loaded[0].Heavy {
		t.Fatalf("List = %+v, %v", loaded, err)
	}
}

func TestLightSnapshotSkipsHeavyParts(t *testing.T) {
	root, dir := newRoot(t), t.TempDir()
	s, err := Take(root, dir, t0, false, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range s.Profiles[0].Files {
		if strings.HasSuffix(f, "/") {
			t.Fatalf("light snapshot copied %s", f)
		}
	}
}

func TestRestorePutsWipedProfileBack(t *testing.T) {
	root, dir := newRoot(t), t.TempDir()
	s, err := Take(root, dir, t0, true, "")
	if err != nil {
		t.Fatal(err)
	}
	// Wipe Profile 1 the way this morning did: empty cookies, reset prefs.
	p1 := filepath.Join(root, "Profile 1")
	writeCookies(t, filepath.Join(p1, "Cookies"), 3)
	writeJSON(t, filepath.Join(p1, "Preferences"), map[string]any{})
	_ = os.RemoveAll(filepath.Join(p1, "Local Extension Settings"))
	// Default changed after the snapshot and must not be touched.
	writeCookies(t, filepath.Join(root, "Default", "Cookies"), 310)

	done, err := Restore(s, root, []string{"Profile 1"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 5 {
		t.Fatalf("restored %v", done)
	}
	if n, _ := countCookies(filepath.Join(p1, "Cookies")); n != 250 {
		t.Fatalf("Profile 1 cookies = %d, want 250", n)
	}
	if _, err := os.Stat(filepath.Join(p1, "Cookies-wal")); !os.IsNotExist(err) {
		t.Fatalf("stale WAL left behind: %v", err)
	}
	exts, _ := Extensions(filepath.Join(p1, "Preferences"))
	if len(exts) != 4 {
		t.Fatalf("Profile 1 extensions = %v", exts)
	}
	if b, err := os.ReadFile(filepath.Join(p1, "Local Extension Settings", "abc", "000003.log")); err != nil || string(b) != "pairing" {
		t.Fatalf("extension storage = %q, %v", b, err)
	}
	if n, _ := countCookies(filepath.Join(root, "Default", "Cookies")); n != 310 {
		t.Fatalf("Default was touched: %d cookies", n)
	}
}

func TestRestoreUnknownProfileFails(t *testing.T) {
	root, dir := newRoot(t), t.TempDir()
	s, _ := Take(root, dir, t0, false, "")
	if _, err := Restore(s, root, []string{"Profile 7"}, false); err == nil {
		t.Fatal("want an error for a profile the snapshot lacks")
	}
}

func TestWarningsSpotAWipe(t *testing.T) {
	prev := Manifest{Profiles: []Profile{
		{Dir: "Default", Cookies: 17000, Extensions: make([]Extension, 12)},
		{Dir: "Profile 1", Name: "Main", Cookies: 900, Extensions: make([]Extension, 4)},
		{Dir: "Profile 2", Cookies: 10},
	}}
	cur := Manifest{Profiles: []Profile{
		{Dir: "Default", Cookies: 40, Extensions: make([]Extension, 1)},
		{Dir: "Profile 1", Name: "Main", Cookies: 880, Extensions: make([]Extension, 4)},
	}}
	got := Warnings(prev, cur)
	want := []string{
		"Chrome profile Default dropped from 17000 cookies to 40",
		"Chrome profile Default dropped from 12 extensions to 1",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("warnings = %q", got)
	}
	cur.Profiles = cur.Profiles[:1]
	if got := Warnings(prev, cur); len(got) != 3 || got[2] != "Chrome profile Main (Profile 1) is gone" {
		t.Fatalf("warnings = %q", got)
	}
}

func TestPlan(t *testing.T) {
	if due, heavy := Plan(nil, t0); !due || !heavy {
		t.Fatal("first snapshot must be due and full")
	}
	snaps := []Snapshot{{Manifest: Manifest{Taken: t0.Add(-30 * time.Minute)}}, {Manifest: Manifest{Taken: t0.Add(-5 * time.Hour), Heavy: true}}}
	if due, _ := Plan(snaps, t0); due {
		t.Fatal("not due 30 minutes after the last one")
	}
	if due, heavy := Plan(snaps, t0.Add(31*time.Minute)); !due || heavy {
		t.Fatal("due after an hour, light while a full one is recent")
	}
	if _, heavy := Plan(snaps, t0.Add(19*time.Hour)); !heavy {
		t.Fatal("a full snapshot is due once a day")
	}
}

func TestRunDuePinsTheGoodSnapshotOnWipe(t *testing.T) {
	root, dir := newRoot(t), t.TempDir()
	if _, err := RunDue(root, dir, t0); err != nil {
		t.Fatal(err)
	}
	if res, _ := RunDue(root, dir, t0.Add(10*time.Minute)); res.Taken != nil {
		t.Fatal("took a snapshot before one was due")
	}
	writeCookies(t, filepath.Join(root, "Default", "Cookies"), 2)
	res, err := RunDue(root, dir, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 1 || res.Good == nil || res.Good.ID != "20261117T120000Z" {
		t.Fatalf("result = %+v", res)
	}
	snaps, _ := List(dir)
	if !snaps[1].Keep {
		t.Fatal("snapshot before the wipe was not pinned")
	}
	good, err := LastGood(snaps)
	if err != nil || good.ID != "20261117T120000Z" {
		t.Fatalf("LastGood = %s, %v", good.ID, err)
	}
}

func TestPruneRetention(t *testing.T) {
	dir := t.TempDir()
	mk := func(at time.Time, heavy, keep bool) string {
		m := Manifest{ID: at.UTC().Format(idLayout), Taken: at.UTC(), Heavy: heavy, Keep: keep}
		p := filepath.Join(dir, m.ID)
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := writeManifest(p, m); err != nil {
			t.Fatal(err)
		}
		return m.ID
	}
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.Local)
	recent := mk(now.Add(-23*time.Hour), false, false)
	dayNewest := mk(now.Add(-3*24*time.Hour+2*time.Hour), false, false)
	dayHeavy := mk(now.Add(-3*24*time.Hour), true, false)
	dayOther := mk(now.Add(-3*24*time.Hour-time.Hour), false, false)
	old := mk(now.Add(-8*24*time.Hour), true, false)
	pinned := mk(now.Add(-20*24*time.Hour), false, true)
	tooOldPinned := mk(now.Add(-31*24*time.Hour), false, true)
	_ = os.MkdirAll(filepath.Join(dir, "x.partial"), 0o700)
	_ = os.Chtimes(filepath.Join(dir, "x.partial"), now.Add(-2*time.Hour), now.Add(-2*time.Hour))

	removed, err := Prune(dir, now)
	if err != nil {
		t.Fatal(err)
	}
	gone := map[string]bool{}
	for _, id := range removed {
		gone[id] = true
	}
	for _, id := range []string{recent, dayNewest, dayHeavy, pinned} {
		if gone[id] {
			t.Errorf("pruned %s, which should be kept", id)
		}
	}
	for _, id := range []string{dayOther, old, tooOldPinned} {
		if !gone[id] {
			t.Errorf("kept %s, which should be pruned", id)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "x.partial")); !os.IsNotExist(err) {
		t.Error("left an old unfinished snapshot")
	}
}

func TestExtensionsReadsBothPrefsFiles(t *testing.T) {
	d := t.TempDir()
	writeJSON(t, filepath.Join(d, "Preferences"), map[string]any{"extensions": map[string]any{"settings": map[string]any{
		"one": map[string]any{"location": 1, "manifest": map[string]any{"name": "One"}},
	}}})
	writeJSON(t, filepath.Join(d, "Secure Preferences"), map[string]any{"extensions": map[string]any{"settings": map[string]any{
		"two":  map[string]any{"location": 1, "disable_reasons": []int{1}, "manifest": map[string]any{"name": "Two"}},
		"comp": map[string]any{"location": 5},
	}}})
	got, err := Extensions(filepath.Join(d, "Preferences"), filepath.Join(d, "Secure Preferences"))
	if err != nil || len(got) != 2 || got[0].ID != "one" || !got[0].Enabled || got[1].ID != "two" || got[1].Enabled {
		t.Fatalf("Extensions = %+v, %v", got, err)
	}
}

func TestNetworkCookiesPathIsBackedUpAndRestored(t *testing.T) {
	root, dir := newRoot(t), t.TempDir()
	p1 := filepath.Join(root, "Profile 1")
	if err := os.MkdirAll(filepath.Join(p1, "Network"), 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(p1, "Cookies"))
	writeCookies(t, filepath.Join(p1, "Network", "Cookies"), 400)
	s, err := Take(root, dir, t0, false, "")
	if err != nil {
		t.Fatal(err)
	}
	got := s.Profiles[1]
	if got.Cookies != 400 || got.Files[0] != filepath.Join("Network", "Cookies") {
		t.Fatalf("Profile 1 = %+v", got)
	}
	writeCookies(t, filepath.Join(p1, "Network", "Cookies"), 1)
	if _, err := Restore(s, root, []string{"Profile 1"}, false); err != nil {
		t.Fatal(err)
	}
	if n, _ := countCookies(filepath.Join(p1, "Network", "Cookies")); n != 400 {
		t.Fatalf("restored %d cookies, want 400", n)
	}
}

func TestDeletedCookieDatabaseCountsAsAWipe(t *testing.T) {
	root, dir := newRoot(t), t.TempDir()
	if _, err := RunDue(root, dir, t0); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(root, "Default", "Cookies"))
	res, err := RunDue(root, dir, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if res.Taken.Profiles[0].Cookies != 0 || len(res.Warnings) != 1 || res.Good == nil {
		t.Fatalf("result = %+v, profile = %+v", res, res.Taken.Profiles[0])
	}
}

func TestVanishedProfilesStillSnapshotAndRestore(t *testing.T) {
	root, dir := newRoot(t), t.TempDir()
	if _, err := RunDue(root, dir, t0); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"Default", "Profile 1"} {
		_ = os.RemoveAll(filepath.Join(root, p))
	}
	res, err := RunDue(root, dir, t0.Add(time.Hour))
	if err != nil {
		t.Fatalf("snapshot of an empty Chrome folder failed: %v", err)
	}
	if len(res.Warnings) != 2 {
		t.Fatalf("warnings = %q", res.Warnings)
	}
	if _, err := Restore(*res.Good, root, nil, true); err != nil {
		t.Fatal(err)
	}
	if n, _ := countCookies(filepath.Join(root, "Default", "Cookies")); n != 300 {
		t.Fatalf("Default cookies = %d after restore", n)
	}
}

func TestSnapshotsInTheSameSecondGetTheirOwnFolders(t *testing.T) {
	root, dir := newRoot(t), t.TempDir()
	done := make(chan Snapshot, 2)
	for range 2 {
		go func() {
			s, err := Take(root, dir, t0, false, "")
			if err != nil {
				t.Error(err)
			}
			done <- s
		}()
	}
	a, b := <-done, <-done
	if a.ID == b.ID {
		t.Fatalf("both snapshots are %s", a.ID)
	}
	snaps, _ := List(dir)
	if len(snaps) != 2 {
		t.Fatalf("listed %d snapshots", len(snaps))
	}
	for _, s := range snaps {
		if len(s.Profiles) != 2 || len(s.Profiles[0].Errors) != 0 {
			t.Fatalf("snapshot %s = %+v", s.ID, s.Profiles)
		}
	}
}

func TestDamagedDatabaseIsStoredAsIs(t *testing.T) {
	root, dir := newRoot(t), t.TempDir()
	copyRetryWait = 0
	bad := filepath.Join(root, "Default", "Cookies")
	if err := os.WriteFile(bad, []byte("not a database at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Take(root, dir, t0, false, "")
	if err != nil {
		t.Fatal(err)
	}
	d := s.Profiles[0]
	if len(d.Errors) != 0 || len(d.Notes) == 0 || d.Files[0] != "Cookies" {
		t.Fatalf("Default = %+v", d)
	}
	if err := os.WriteFile(bad, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(s, root, []string{"Default"}, false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(bad); string(b) != "not a database at all" {
		t.Fatalf("restored %q", b)
	}
}
