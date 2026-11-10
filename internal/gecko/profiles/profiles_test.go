package profiles

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func put(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}
func fixture(t *testing.T) (Resolver, string) {
	t.Helper()
	home := t.TempDir()
	return Resolver{HomeDir: home, GOOS: "linux"}, filepath.Join(home, ".mozilla/firefox")
}
func profile(t *testing.T, root, name string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	put(t, filepath.Join(dir, "cookies.sqlite"), "")
	put(t, filepath.Join(dir, "times.json"), `{"created":123456789}`)
	return dir
}
func TestInstallSelection(t *testing.T) {
	r, root := fixture(t)
	first := profile(t, root, "a")
	second := profile(t, root, "b")
	put(t, filepath.Join(root, "profiles.ini"), fmt.Sprintf("[Profile0]\nName=wrong\nIsRelative=1\nPath=a\nDefault=1\n[Profile1]\nName=right\nIsRelative=1\nPath=b\n[InstallBAD]\nDefault=a\nLocked=1\n[Install%s]\nDefault=b\nLocked=0\n", InstallHash("/usr/lib/firefox")))
	got, err := r.Resolve("firefox", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Dir != second || got.Name != "right" || got.How != "install" || got.Created != 123456789 || got.InstallHash != InstallHash("/usr/lib/firefox") {
		t.Fatalf("identity: %+v", got)
	}
	for selector, want := range map[string]string{"wrong": first, "a": first, second: second} {
		got, err = r.Resolve("firefox", selector)
		if err != nil || got.Dir != want {
			t.Errorf("%q: %+v %v", selector, got, err)
		}
	}
}
func TestNoMatchAndStaleRequireSelector(t *testing.T) {
	for _, target := range []string{"a", "gone"} {
		t.Run(target, func(t *testing.T) {
			r, root := fixture(t)
			profile(t, root, "a")
			put(t, filepath.Join(root, "a/compatibility.ini"), "[Compatibility]\nLastAppDir=/old/app\n")
			hash := "BAD"
			if target == "gone" {
				hash = InstallHash("/usr/lib/firefox")
			}
			put(t, filepath.Join(root, "profiles.ini"), fmt.Sprintf("[Profile0]\nName=only\nIsRelative=1\nPath=a\nDefault=1\n[Install%s]\nDefault=%s\nLocked=1\n", hash, target))
			_, err := r.Resolve("firefox", "")
			if err == nil || !strings.Contains(err.Error(), "browser.profile") || !strings.Contains(err.Error(), "/old/app") || !strings.Contains(err.Error(), "Profile0") {
				t.Fatalf("diagnostic: %v", err)
			}
			if _, err = r.Resolve("firefox", "only"); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestDuplicateNamesAndBasenames(t *testing.T) {
	r, root := fixture(t)
	profile(t, root, "x/a")
	profile(t, root, "y/a")
	put(t, filepath.Join(root, "profiles.ini"), "[Profile0]\nName=dup\nPath=x/a\nIsRelative=1\n[Profile1]\nName=dup\nPath=y/a\nIsRelative=1\n")
	for _, s := range []string{"dup", "a"} {
		_, err := r.Resolve("firefox", s)
		if err == nil || !strings.Contains(err.Error(), "x/a") || !strings.Contains(err.Error(), "y/a") {
			t.Fatalf("ambiguity %q: %v", s, err)
		}
	}
}
func TestExplicitPathAndIdentity(t *testing.T) {
	r, root := fixture(t)
	dir := profile(t, root, "p")
	link := filepath.Join(root, "alias")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	got, err := r.Resolve("firefox", link)
	if err != nil || got.Dir != dir || got.How != "path" {
		t.Fatalf("%+v %v", got, err)
	}
	for _, changed := range []ProfileIdentity{{Dir: dir + "new", Created: got.Created}, {Dir: dir, Created: got.Created + 1}} {
		if ValidateIdentity(got, changed, false, false) == nil {
			t.Fatal("identity change accepted")
		}
		if err := ValidateIdentity(got, changed, true, false); err != nil {
			t.Fatal(err)
		}
		if err := ValidateIdentity(got, changed, false, true); err != nil {
			t.Fatal(err)
		}
	}
	if err := ValidateIdentity(ProfileIdentity{}, got, false, false); err != nil {
		t.Fatal(err)
	}
	if err := ValidateIdentity(got, got, false, false); err != nil {
		t.Fatal(err)
	}
}
func TestStorageOnlyAndInvalidProfiles(t *testing.T) {
	r, _ := fixture(t)
	dir := t.TempDir()
	if _, err := r.Resolve("firefox", dir); err == nil {
		t.Fatal("empty directory accepted")
	}
	if err := os.MkdirAll(filepath.Join(dir, "storage/default"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve("firefox", dir); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(dir, "times.json"), "broken")
	if _, err := r.Resolve("firefox", dir); err == nil {
		t.Fatal("malformed identity accepted")
	}
}
func TestAppOverrideAndMacCandidates(t *testing.T) {
	r, root := fixture(t)
	profile(t, root, "a")
	r.AppPaths = []string{"/opt/custom/firefox"}
	put(t, filepath.Join(root, "profiles.ini"), fmt.Sprintf("[Profile0]\nName=a\nPath=a\nIsRelative=1\n[Install%s]\nDefault=a\n", InstallHash("/usr/lib/firefox")))
	if _, err := r.Resolve("firefox", ""); err == nil {
		t.Fatal("override fell back to registry")
	}
	r.GOOS = "darwin"
	r.AppPaths = []string{"/Applications/Custom.app"}
	dirs, err := r.InstallDirectories("zen")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/Applications/Custom.app/Contents/MacOS", "/Applications/Custom.app/Contents/Resources"}
	if strings.Join(dirs, "|") != strings.Join(want, "|") {
		t.Fatalf("candidates %v", dirs)
	}
}
func TestLinuxPackagedRoots(t *testing.T) {
	for _, rel := range []string{"snap/firefox/common/.mozilla/firefox", ".var/app/org.mozilla.firefox/.mozilla/firefox"} {
		t.Run(rel, func(t *testing.T) {
			r, _ := fixture(t)
			root := filepath.Join(r.HomeDir, rel)
			dir := profile(t, root, "packaged")
			put(t, filepath.Join(root, "profiles.ini"), "[Profile0]\nName=packaged\nPath=packaged\nIsRelative=1\n")
			got, err := r.Resolve("firefox", "packaged")
			if err != nil || got.Dir != dir {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
}

func TestAmbiguousInstallsAndNamePrecedence(t *testing.T) {
	r, root := fixture(t)
	a := profile(t, root, "a")
	profile(t, root, "named")
	put(t, filepath.Join(root, "profiles.ini"), fmt.Sprintf("[Profile0]\nName=named\nPath=a\nIsRelative=1\n[Profile1]\nName=other\nPath=named\nIsRelative=1\n[Install%s]\nDefault=a\n[Install%s]\nDefault=named\n", InstallHash("/usr/lib/firefox"), InstallHash("/opt/firefox")))
	if _, err := r.Resolve("firefox", ""); err == nil {
		t.Fatal("ambiguous installations selected")
	}
	got, err := r.Resolve("firefox", "named")
	if err != nil || got.Dir != a || got.How != "name" {
		t.Fatalf("name precedence: %+v %v", got, err)
	}
}
func TestMacInstallSelection(t *testing.T) {
	for _, part := range []string{"MacOS", "Resources"} {
		t.Run(part, func(t *testing.T) {
			r, _ := fixture(t)
			r.GOOS = "darwin"
			root := filepath.Join(r.HomeDir, "Library/Application Support/zen")
			dir := profile(t, root, "Profiles/one")
			put(t, filepath.Join(root, "profiles.ini"), fmt.Sprintf("[Profile0]\nName=one\nPath=Profiles/one\nIsRelative=1\n[Install%s]\nDefault=Profiles/one\n", InstallHash("/Applications/Zen.app/Contents/"+part)))
			got, err := r.Resolve("zen", "")
			if err != nil || got.Dir != dir {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
}
func TestINIAndCandidateDiagnostics(t *testing.T) {
	for _, contents := range []string{"[Profile0]\nName=a\nName=b\n", "[Profile0]\n[Profile0]\n", "[Profile0]\nPath=a\n", "[Profile0]\nName=a\n", "bad"} {
		t.Run(contents, func(t *testing.T) {
			r, root := fixture(t)
			put(t, filepath.Join(root, "profiles.ini"), contents)
			if _, err := r.Resolve("firefox", ""); err == nil {
				t.Fatal("malformed profile registry accepted")
			}
		})
	}
	r, root := fixture(t)
	dir := profile(t, root, "a")
	put(t, filepath.Join(root, "a/compatibility.ini"), "[Compatibility]\nLastAppDir=/application\n")
	put(t, filepath.Join(root, "profiles.ini"), fmt.Sprintf("[Profile0]\nName=a\nPath=%s\nIsRelative=0\n[InstallABC]\nDefault=%s\n", dir, dir))
	c, err := r.Candidates("firefox")
	if err != nil || len(c) != 1 || c[0].LastAppDir != "/application" || strings.Join(c[0].Sections, ",") != "Profile0,InstallABC" {
		t.Fatalf("%+v %v", c, err)
	}
}
func TestExecutableSymlinkAndMissingMetadata(t *testing.T) {
	r, _ := fixture(t)
	app := filepath.Join(t.TempDir(), "bin/firefox")
	put(t, app, "fixture executable")
	link := filepath.Join(t.TempDir(), "launcher")
	if err := os.Symlink(app, link); err != nil {
		t.Fatal(err)
	}
	r.AppPaths = []string{link}
	dirs, err := r.InstallDirectories("firefox")
	if err != nil || len(dirs) != 1 || dirs[0] != filepath.Dir(app) {
		t.Fatalf("%v %v", dirs, err)
	}
	dir := t.TempDir()
	put(t, filepath.Join(dir, "cookies.sqlite"), "")
	got, err := r.Resolve("firefox", dir)
	if err != nil || got.Created != 0 {
		t.Fatalf("missing times: %+v %v", got, err)
	}
	if _, err := r.Resolve("chrome", dir); err == nil {
		t.Fatal("Chromium accepted")
	}
}

// A configured selector is reused across unattended runs. Its presence is not
// a new authorization to switch profiles when the name is reassigned.
func TestConfiguredSelectorDoesNotAuthorizeIdentityChange(t *testing.T) {
	for _, change := range []string{"directory", "creation time"} {
		t.Run(change, func(t *testing.T) {
			r, root := fixture(t)
			profile(t, root, "a")
			profile(t, root, "b")
			ini := filepath.Join(root, "profiles.ini")
			put(t, ini, "[Profile0]\nName=configured\nPath=a\nIsRelative=1\n")
			previous, err := r.Resolve("firefox", "configured")
			if err != nil {
				t.Fatal(err)
			}
			if change == "directory" {
				put(t, ini, "[Profile0]\nName=configured\nPath=b\nIsRelative=1\n")
			} else {
				put(t, filepath.Join(root, "a/times.json"), `{"created":987654321}`)
			}
			current, err := r.Resolve("firefox", "configured")
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateIdentity(previous, current, false, false); err == nil {
				t.Fatal("same persisted selector silently authorized replacement")
			}
			if err := ValidateIdentity(previous, current, true, false); err != nil {
				t.Fatal(err)
			}
		})
	}
}
