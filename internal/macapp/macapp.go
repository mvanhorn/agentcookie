// Package macapp installs agentcookie.app, the small signed app agentcookie's
// LaunchAgents on macOS start through (cmd/agentcookie-launcher), so macOS
// lists them in Login Items as "agentcookie" with its icon instead of under
// the signing certificate's personal name.
//
// Release builds for macOS embed the signed, notarized and stapled app as a
// zip (build tag macapp, see `make mac-app`). Ensure installs it into
// ~/Applications, which needs no admin password. It replaces an installed copy
// only when that copy is missing, fails its signature check, or carries a
// lower AppRevision than the embedded one, so two release binaries on one Mac
// never swap the app back and forth. Other builds embed nothing.
//
// This package must stay a small leaf: the launcher imports it.
package macapp

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

const (
	// BundleID identifies agentcookie.app. Wrapped LaunchAgents name it in
	// AssociatedBundleIdentifiers.
	BundleID = "dev.agentcookie.app"
	// AppName is the bundle's directory name in ~/Applications.
	AppName = "agentcookie.app"
	// LauncherName is the launcher executable's file name inside the bundle.
	LauncherName = "agentcookie-launcher"
	// launcherRel is the launcher's path inside the bundle.
	launcherRel = "Contents/MacOS/" + LauncherName

	// AppRevision is the bundle's CFBundleVersion. Bump it whenever the
	// launcher, Info.plist or icon changes, so installed copies get replaced.
	AppRevision = 1

	developerID = `anchor apple generic and certificate 1[field.1.2.840.113635.100.6.2.6] exists and certificate leaf[field.1.2.840.113635.100.6.1.13] exists and certificate leaf[subject.OU] = "NM8VT393AR"`

	// TargetRequirement is what the launcher demands of the binary it runs:
	// Apple's Developer ID chain, the agentcookie team, and the "agentcookie"
	// signing identifier scripts/sign.sh produces. The Team ID alone is not
	// enough, since a self-signed certificate can carry any OU.
	TargetRequirement = developerID + ` and identifier "agentcookie"`

	// AppRequirement is what an installed agentcookie.app must satisfy.
	AppRequirement = developerID + ` and identifier "` + BundleID + `"`

	lsregister = "/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister"
)

// ErrUnavailable means this agentcookie cannot install the app: it is not
// running on macOS, or it is a build without the embedded app.
var ErrUnavailable = errors.New("this build does not include agentcookie.app")

// Path is the installed app's location for home.
func Path(home string) string { return filepath.Join(home, "Applications", AppName) }

// Launcher is the installed launcher's path for home.
func Launcher(home string) string { return filepath.Join(Path(home), launcherRel) }

// IsLauncher reports whether path is a launcher inside an agentcookie.app
// bundle, wherever that bundle lives.
func IsLauncher(path string) bool {
	return filepath.Base(path) == LauncherName &&
		strings.HasSuffix(filepath.Dir(path), filepath.Join(AppName, "Contents", "MacOS"))
}

// Options configures Ensure and Wrapper. Zero values mean the running system:
// the user's home, runtime.GOOS, the embedded zip, codesign checks and
// lsregister.
type Options struct {
	Home     string
	GOOS     string
	Zip      []byte
	Validate func(app string) error
	Verify   func(target string) error
	Register func(app string) error
	// Swap exchanges two paths in one step. It defaults to RENAME_SWAP,
	// which only macOS has; tests on other systems supply their own.
	Swap func(a, b string) error
}

func (o Options) withDefaults() (Options, error) {
	if o.GOOS == "" {
		o.GOOS = runtime.GOOS
	}
	if o.Zip == nil {
		o.Zip = embeddedZip
	}
	if o.Home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return o, err
		}
		o.Home = h
	}
	if o.Validate == nil {
		o.Validate = Validate
	}
	if o.Verify == nil {
		o.Verify = VerifyTarget
	}
	if o.Register == nil {
		o.Register = register
	}
	if o.Swap == nil {
		o.Swap = swap
	}
	return o, nil
}

// Ensure installs or updates ~/Applications/agentcookie.app from the embedded
// zip. It returns ErrUnavailable off macOS or without an embedded app. A
// failed unpack or signature check leaves any installed app untouched.
func Ensure(o Options) error {
	o, err := o.withDefaults()
	if err != nil {
		return err
	}
	if o.GOOS != "darwin" || len(o.Zip) == 0 {
		return ErrUnavailable
	}
	zr, err := zip.NewReader(bytes.NewReader(o.Zip), int64(len(o.Zip)))
	if err != nil {
		return fmt.Errorf("embedded agentcookie.app: %w", err)
	}
	embedded, err := zipRevision(zr)
	if err != nil {
		return fmt.Errorf("embedded agentcookie.app: %w", err)
	}
	dst := Path(o.Home)
	if installed, err := installedRevision(dst); err == nil && installed >= embedded && o.Validate(dst) == nil {
		return nil
	}
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(dir, ".agentcookie-app-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := extract(zr, tmp); err != nil {
		return fmt.Errorf("unpack agentcookie.app: %w", err)
	}
	staged := filepath.Join(tmp, AppName)
	if err := o.Validate(staged); err != nil {
		return fmt.Errorf("unpacked agentcookie.app: %w", err)
	}
	if _, err := os.Lstat(dst); err == nil {
		// Swap in one step so the launcher path never goes missing while
		// launchd may be starting a service. The old copy lands in tmp.
		if err := o.Swap(staged, dst); err != nil {
			return fmt.Errorf("replace %s: %w", dst, err)
		}
	} else if err := os.Rename(staged, dst); err != nil {
		return err
	}
	if err := o.Register(dst); err != nil {
		return fmt.Errorf("register %s: %w", dst, err)
	}
	return nil
}

// Wrapper returns the launcher a LaunchAgent running target should start
// through, or "" when it should run target directly. It first installs or
// updates the app when this build embeds one, then requires a valid installed
// app and a target the launcher would accept. Keying on the installed app,
// not on this build, lets a signed `make install` build reuse the app a
// release left behind, and falls back to a bare plist when either check
// fails, so a wrapped job never meets a launcher that refuses its target.
func Wrapper(o Options, target string) string {
	o, err := o.withDefaults()
	if err != nil || o.GOOS != "darwin" {
		return ""
	}
	app := Path(o.Home)
	// A nil Ensure has already validated the installed or new app.
	ensureErr := Ensure(o)
	if _, err := os.Stat(Launcher(o.Home)); err != nil {
		return ""
	}
	if ensureErr != nil && o.Validate(app) != nil {
		return ""
	}
	if o.Verify(target) != nil {
		return ""
	}
	return Launcher(o.Home)
}

// Remove deletes the installed app, for uninstall.
func Remove(home string) error {
	if err := os.RemoveAll(Path(home)); err != nil {
		return fmt.Errorf("remove %s: %w", Path(home), err)
	}
	return nil
}

var bundleVersionRE = regexp.MustCompile(`<key>CFBundleVersion</key>\s*<string>\s*(\d+)\s*</string>`)

// plistRevision reads the integer CFBundleVersion from an Info.plist.
func plistRevision(b []byte) (int, error) {
	m := bundleVersionRE.FindSubmatch(b)
	if m == nil {
		return 0, errors.New("no integer CFBundleVersion in Info.plist")
	}
	return strconv.Atoi(string(m[1]))
}

func installedRevision(app string) (int, error) {
	b, err := os.ReadFile(filepath.Join(app, "Contents", "Info.plist"))
	if err != nil {
		return 0, err
	}
	return plistRevision(b)
}

func zipRevision(zr *zip.Reader) (int, error) {
	for _, f := range zr.File {
		if f.Name == AppName+"/Contents/Info.plist" {
			b, err := readEntry(f)
			if err != nil {
				return 0, err
			}
			return plistRevision(b)
		}
	}
	return 0, errors.New("zip has no Contents/Info.plist")
}

// entryPath maps a zip entry to its path inside the bundle.
func entryPath(name string) (string, bool) {
	rel, ok := strings.CutPrefix(name, AppName+"/")
	return filepath.FromSlash(rel), ok
}

// extract writes the zip's app into dir. The zip is build-time content inside
// the signed agentcookie binary; the signature check on the result is what
// guards the install.
func extract(zr *zip.Reader, dir string) error {
	for _, f := range zr.File {
		rel, ok := entryPath(f.Name)
		if !ok {
			return fmt.Errorf("unexpected entry %q", f.Name)
		}
		p := filepath.Join(dir, AppName, rel)
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
			continue
		}
		b, err := readEntry(f)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, b, f.Mode().Perm()|0o400); err != nil {
			return err
		}
	}
	return nil
}

func readEntry(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// Validate checks app's code signature against AppRequirement.
func Validate(app string) error {
	if err := codesignVerify("--deep", "-R="+AppRequirement, app); err != nil {
		return fmt.Errorf("signature check failed: %w", err)
	}
	return nil
}

// VerifyTarget checks that exe is a release-signed agentcookie
// (TargetRequirement), the check the launcher makes before running it.
func VerifyTarget(exe string) error {
	return codesignVerify("-R="+TargetRequirement, exe)
}

func codesignVerify(args ...string) error {
	out, err := exec.Command("/usr/bin/codesign", append([]string{"--verify", "--strict"}, args...)...).CombinedOutput()
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return errors.New(msg)
		}
		return err
	}
	return nil
}

func register(app string) error {
	if out, err := exec.Command(lsregister, "-f", app).CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
