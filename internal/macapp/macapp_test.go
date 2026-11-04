package macapp

import (
	"archive/zip"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// testZip builds an app zip shaped like `ditto -c -k --keepParent` output:
// every entry under "agentcookie.app/", with the launcher executable.
func testZip(t *testing.T, launcher string, revision int) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name string, mode os.FileMode, body string) {
		h := &zip.FileHeader{Name: AppName + "/" + name, Method: zip.Deflate}
		h.SetMode(mode)
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	add("Contents/Info.plist", 0o644, "<plist><dict><key>CFBundleIdentifier</key><string>"+BundleID+
		"</string><key>CFBundleVersion</key>\n  <string>"+strconv.Itoa(revision)+"</string></dict></plist>")
	add("Contents/MacOS/"+LauncherName, 0o755, launcher)
	add("Contents/Resources/AppIcon.icns", 0o644, "icon")
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type recorder struct {
	registered []string
	rejectApp  func(path string) bool
	rejectExe  bool
}

func (r *recorder) opts(home string, z []byte) Options {
	return Options{
		Home: home,
		GOOS: "darwin",
		Zip:  z,
		Validate: func(app string) error {
			if r.rejectApp != nil && r.rejectApp(app) {
				return errors.New("signature invalid")
			}
			if _, err := os.Stat(filepath.Join(app, launcherRel)); err != nil {
				return err
			}
			return nil
		},
		Verify: func(string) error {
			if r.rejectExe {
				return errors.New("not signed by agentcookie")
			}
			return nil
		},
		Register: func(app string) error {
			r.registered = append(r.registered, app)
			return nil
		},
	}
}

func launcherBody(t *testing.T, home string) string {
	t.Helper()
	b, err := os.ReadFile(Launcher(home))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func noTemps(t *testing.T, home string) {
	t.Helper()
	ents, err := os.ReadDir(filepath.Join(home, "Applications"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if e.Name() != AppName {
			t.Errorf("left behind %s in ~/Applications", e.Name())
		}
	}
}

func TestEnsureInstallsFreshApp(t *testing.T) {
	home := t.TempDir()
	var r recorder
	if err := Ensure(r.opts(home, testZip(t, "v1", 1))); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(Launcher(home))
	if err != nil || fi.Mode().Perm()&0o111 == 0 {
		t.Fatalf("launcher not an executable file: %v %v", fi, err)
	}
	if launcherBody(t, home) != "v1" {
		t.Fatal("launcher content not from the zip")
	}
	if len(r.registered) != 1 || r.registered[0] != Path(home) {
		t.Fatalf("registered %v, want the installed app", r.registered)
	}
	noTemps(t, home)
}

func TestEnsureLeavesSameRevisionAlone(t *testing.T) {
	home := t.TempDir()
	var r recorder
	if err := Ensure(r.opts(home, testZip(t, "v1", 1))); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(Launcher(home), old, old); err != nil {
		t.Fatal(err)
	}
	// Different content, same revision: a second release binary on this Mac.
	if err := Ensure(r.opts(home, testZip(t, "other build", 1))); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(Launcher(home))
	if err != nil {
		t.Fatal(err)
	}
	if !fi.ModTime().Equal(old) || launcherBody(t, home) != "v1" {
		t.Fatal("same-revision app was rewritten")
	}
}

func TestEnsureReplacesLowerRevision(t *testing.T) {
	home := t.TempDir()
	var r recorder
	if err := Ensure(r.opts(home, testZip(t, "v1", 1))); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(r.opts(home, testZip(t, "v2", 2))); err != nil {
		t.Fatal(err)
	}
	if launcherBody(t, home) != "v2" {
		t.Fatal("lower-revision app not replaced")
	}
	noTemps(t, home)
}

func TestEnsureKeepsHigherRevision(t *testing.T) {
	home := t.TempDir()
	var r recorder
	if err := Ensure(r.opts(home, testZip(t, "v3", 3))); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(r.opts(home, testZip(t, "v2", 2))); err != nil {
		t.Fatal(err)
	}
	if launcherBody(t, home) != "v3" {
		t.Fatal("newer installed app was downgraded")
	}
}

func TestEnsureReplacesInvalidApp(t *testing.T) {
	home := t.TempDir()
	var r recorder
	if err := Ensure(r.opts(home, testZip(t, "v1", 1))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Launcher(home), []byte("tampered"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The installed copy now fails validation; the staged one passes.
	r.rejectApp = func(app string) bool { return app == Path(home) }
	if err := Ensure(r.opts(home, testZip(t, "v1", 1))); err != nil {
		t.Fatal(err)
	}
	if launcherBody(t, home) != "v1" {
		t.Fatal("invalid app not replaced")
	}
	noTemps(t, home)
}

func TestEnsureRejectsBadZipAndKeepsInstalledApp(t *testing.T) {
	home := t.TempDir()
	var r recorder
	if err := Ensure(r.opts(home, testZip(t, "v1", 1))); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(r.opts(home, []byte("not a zip"))); err == nil {
		t.Fatal("corrupt zip accepted")
	}
	// A higher-revision zip whose bundle fails validation leaves the old app.
	r.rejectApp = func(app string) bool { return app != Path(home) }
	if err := Ensure(r.opts(home, testZip(t, "v2", 2))); err == nil {
		t.Fatal("invalid staged app accepted")
	}
	if launcherBody(t, home) != "v1" {
		t.Fatal("installed app changed after a failed update")
	}
	noTemps(t, home)
}

func TestEnsureUnavailable(t *testing.T) {
	home := t.TempDir()
	var r recorder
	o := r.opts(home, nil)
	o.Zip = []byte{}
	if err := Ensure(o); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("no zip: err = %v, want ErrUnavailable", err)
	}
	o = r.opts(home, testZip(t, "v1", 1))
	o.GOOS = "linux"
	if err := Ensure(o); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("linux: err = %v, want ErrUnavailable", err)
	}
	if _, err := os.Stat(filepath.Join(home, "Applications")); !os.IsNotExist(err) {
		t.Fatal("Ensure wrote files while unavailable")
	}
}

func TestWrapper(t *testing.T) {
	home := t.TempDir()
	var r recorder
	o := r.opts(home, testZip(t, "v1", 1))
	if got := Wrapper(o, "/bin/agentcookie"); got != Launcher(home) {
		t.Fatalf("fresh install: wrapper = %q, want %q", got, Launcher(home))
	}

	// A build with no embedded app (make install) still uses the installed one.
	noZip := r.opts(home, nil)
	noZip.Zip = []byte{}
	if got := Wrapper(noZip, "/bin/agentcookie"); got != Launcher(home) {
		t.Fatalf("no embedded zip: wrapper = %q, want the installed launcher", got)
	}

	r.rejectExe = true
	if got := Wrapper(noZip, "/bin/agentcookie"); got != "" {
		t.Fatalf("rejected target: wrapper = %q, want none", got)
	}
	r.rejectExe = false

	r.rejectApp = func(string) bool { return true }
	if got := Wrapper(noZip, "/bin/agentcookie"); got != "" {
		t.Fatalf("invalid app: wrapper = %q, want none", got)
	}
	r.rejectApp = nil

	if err := Remove(home); err != nil {
		t.Fatal(err)
	}
	if got := Wrapper(noZip, "/bin/agentcookie"); got != "" {
		t.Fatalf("no app: wrapper = %q, want none", got)
	}
	// A release build reinstalls a trashed app.
	if got := Wrapper(o, "/bin/agentcookie"); got != Launcher(home) {
		t.Fatalf("after removal: wrapper = %q, want the reinstalled launcher", got)
	}
	linux := r.opts(home, testZip(t, "v1", 1))
	linux.GOOS = "linux"
	if got := Wrapper(linux, "/bin/agentcookie"); got != "" {
		t.Fatalf("linux: wrapper = %q, want none", got)
	}
}

func TestIsLauncher(t *testing.T) {
	for path, want := range map[string]bool{
		"/Users/a/Applications/agentcookie.app/Contents/MacOS/agentcookie-launcher": true,
		"/Applications/agentcookie.app/Contents/MacOS/agentcookie-launcher":         true,
		"/Users/a/go/bin/agentcookie":                                               false,
		"/tmp/agentcookie-launcher":                                                 false,
	} {
		if got := IsLauncher(path); got != want {
			t.Errorf("IsLauncher(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestRequirements(t *testing.T) {
	for _, req := range []string{TargetRequirement, AppRequirement} {
		for _, want := range []string{
			"anchor apple generic",
			"certificate 1[field.1.2.840.113635.100.6.2.6] exists",
			"certificate leaf[field.1.2.840.113635.100.6.1.13] exists",
			`certificate leaf[subject.OU] = "NM8VT393AR"`,
		} {
			if !strings.Contains(req, want) {
				t.Errorf("requirement %q lacks %q", req, want)
			}
		}
	}
	if !strings.HasSuffix(TargetRequirement, `identifier "agentcookie"`) {
		t.Errorf("target requirement must pin identifier agentcookie: %s", TargetRequirement)
	}
	if !strings.HasSuffix(AppRequirement, `identifier "dev.agentcookie.app"`) {
		t.Errorf("app requirement must pin the bundle id: %s", AppRequirement)
	}
}

// TestInfoPlistTemplate keeps the bundle template in step with the constants.
func TestInfoPlistTemplate(t *testing.T) {
	b, err := os.ReadFile("Info.plist.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		"<string>" + BundleID + "</string>",
		"<string>" + LauncherName + "</string>",
		"<string>agentcookie</string>",
		"<key>LSUIElement</key>",
		"__REVISION__",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("Info.plist.tmpl lacks %q", want)
		}
	}
	for _, forbidden := range []string{"CFBundleURLTypes", "CFBundleDocumentTypes", "NSServices"} {
		if strings.Contains(s, forbidden) {
			t.Errorf("Info.plist.tmpl declares %s; the app must handle nothing", forbidden)
		}
	}
	rev, err := plistRevision([]byte(strings.ReplaceAll(s, "__REVISION__", strconv.Itoa(AppRevision))))
	if err != nil || rev != AppRevision {
		t.Fatalf("rendered template revision = %d, %v", rev, err)
	}
}

func TestSwapOnDarwin(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("RENAME_SWAP is macOS-only")
	}
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	for p, body := range map[string]string{a: "A", b: "B"} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "f"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := swap(a, b); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(b, "f"))
	if string(got) != "A" {
		t.Fatalf("after swap b holds %q, want A", got)
	}
}
