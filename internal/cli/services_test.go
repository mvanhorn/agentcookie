package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/mvanhorn/agentcookie/internal/launchd"
	"github.com/mvanhorn/agentcookie/internal/macapp"
)

// fakeServiceOps stores plists as JSON, so tests need neither plutil nor
// launchctl.
type fakeServiceOps struct {
	loaded        map[string]bool
	bootouts      []string
	bootstraps    []string
	failBootstrap bool
}

func (f *fakeServiceOps) ReadPlist(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	return m, json.Unmarshal(b, &m)
}

func (f *fakeServiceOps) EditPlist(path string, orig []byte, mode os.FileMode, args, ids []string) error {
	var m map[string]any
	if err := json.Unmarshal(orig, &m); err != nil {
		return err
	}
	m["ProgramArguments"] = args
	if len(ids) == 0 {
		delete(m, "AssociatedBundleIdentifiers")
	} else {
		m["AssociatedBundleIdentifiers"] = ids
	}
	b, _ := json.Marshal(m)
	return writeFileReplacing(path, b, mode)
}

func (f *fakeServiceOps) Loaded(label string) bool { return f.loaded[label] }

func (f *fakeServiceOps) Bootout(label string) error {
	f.bootouts = append(f.bootouts, label)
	f.loaded[label] = false
	return nil
}

func (f *fakeServiceOps) Bootstrap(path string) error {
	f.bootstraps = append(f.bootstraps, path)
	if f.failBootstrap {
		return errors.New("bootstrap failed")
	}
	f.loaded[strings.TrimSuffix(filepath.Base(path), ".plist")] = true
	return nil
}

type refreshFixture struct {
	home     string
	agents   string
	ops      *fakeServiceOps
	out      *bytes.Buffer
	launcher string
	// accept is the set of targets the wrap decision accepts.
	accept map[string]bool
}

func newRefreshFixture(t *testing.T) *refreshFixture {
	t.Helper()
	home := t.TempDir()
	f := &refreshFixture{
		home:     home,
		agents:   filepath.Join(home, "Library", "LaunchAgents"),
		ops:      &fakeServiceOps{loaded: map[string]bool{}},
		out:      &bytes.Buffer{},
		launcher: macapp.Launcher(home),
		accept:   map[string]bool{"/h/bin/agentcookie": true},
	}
	if err := os.MkdirAll(f.agents, 0o755); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *refreshFixture) refresher() *serviceRefresher {
	return &serviceRefresher{
		home: f.home,
		launcherFor: func(role launchd.Role, target string) string {
			return launchd.LauncherFor(launchd.Wrapping{Home: f.home, Wrapper: func(t string) string {
				if f.accept[t] {
					return f.launcher
				}
				return ""
			}}, role, target)
		},
		ops:   f.ops,
		sleep: func() {},
		out:   f.out,
	}
}

func (f *refreshFixture) write(t *testing.T, name string, m map[string]any) string {
	t.Helper()
	b, _ := json.Marshal(m)
	p := filepath.Join(f.agents, name)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func (f *refreshFixture) read(t *testing.T, label string) map[string]any {
	t.Helper()
	m, err := f.ops.ReadPlist(filepath.Join(f.agents, label+".plist"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func argsOf(t *testing.T, m map[string]any) []string {
	t.Helper()
	a, ok := stringList(m["ProgramArguments"])
	if !ok {
		t.Fatalf("no ProgramArguments in %v", m)
	}
	return a
}

func bare(label, role string, extra ...string) map[string]any {
	args := []any{"/h/bin/agentcookie", role}
	for _, e := range extra {
		args = append(args, e)
	}
	return map[string]any{"Label": label, "ProgramArguments": args, "RunAtLoad": true}
}

func TestRefreshWrapsBareSourceAndKeepsExtraKeys(t *testing.T) {
	f := newRefreshFixture(t)
	m := bare("dev.agentcookie.source", "source", "--watch")
	m["EnvironmentVariables"] = map[string]any{"FOO": "bar"}
	f.write(t, "dev.agentcookie.source.plist", m)
	f.ops.loaded["dev.agentcookie.source"] = true

	if err := f.refresher().run(); err != nil {
		t.Fatal(err)
	}
	got := f.read(t, "dev.agentcookie.source")
	if want := []string{f.launcher, "/h/bin/agentcookie", "source", "--watch"}; !slices.Equal(argsOf(t, got), want) {
		t.Errorf("ProgramArguments = %v, want %v", argsOf(t, got), want)
	}
	if ids, _ := stringList(got["AssociatedBundleIdentifiers"]); !slices.Equal(ids, []string{macapp.BundleID}) {
		t.Errorf("AssociatedBundleIdentifiers = %v", ids)
	}
	if got["EnvironmentVariables"] == nil {
		t.Error("refresh dropped a key the user added")
	}
	if !slices.Equal(f.ops.bootouts, []string{"dev.agentcookie.source"}) || len(f.ops.bootstraps) != 1 {
		t.Errorf("restart: bootouts=%v bootstraps=%v", f.ops.bootouts, f.ops.bootstraps)
	}
	if !strings.Contains(f.out.String(), "dev.agentcookie.source: wrapped") || !strings.Contains(f.out.String(), "restarted") {
		t.Errorf("output = %q", f.out.String())
	}
}

func TestRefreshLeavesWrappedPlistAlone(t *testing.T) {
	f := newRefreshFixture(t)
	f.write(t, "dev.agentcookie.cmux-sync.plist", map[string]any{
		"Label":                       "dev.agentcookie.cmux-sync",
		"ProgramArguments":            []any{f.launcher, "/h/bin/agentcookie", "cmux-sync", "--watch"},
		"AssociatedBundleIdentifiers": []any{macapp.BundleID},
	})
	f.ops.loaded["dev.agentcookie.cmux-sync"] = true
	if err := f.refresher().run(); err != nil {
		t.Fatal(err)
	}
	if len(f.ops.bootouts) != 0 {
		t.Errorf("restarted an unchanged job: %v", f.ops.bootouts)
	}
	if !strings.Contains(f.out.String(), "dev.agentcookie.cmux-sync: unchanged") {
		t.Errorf("output = %q", f.out.String())
	}
}

func TestRefreshUnwrapsRejectedTargetAndSink(t *testing.T) {
	f := newRefreshFixture(t)
	f.accept = map[string]bool{} // the target no longer passes the requirement
	f.write(t, "dev.agentcookie.source.plist", map[string]any{
		"Label":                       "dev.agentcookie.source",
		"ProgramArguments":            []any{f.launcher, "/h/bin/agentcookie", "source", "--watch"},
		"AssociatedBundleIdentifiers": []any{"com.example.other", macapp.BundleID},
	})
	f.write(t, "dev.agentcookie.sink.plist", map[string]any{
		"Label":                       "dev.agentcookie.sink",
		"ProgramArguments":            []any{f.launcher, "/h/bin/agentcookie", "sink"},
		"AssociatedBundleIdentifiers": []any{macapp.BundleID},
	})
	f.ops.loaded["dev.agentcookie.source"] = true
	if err := f.refresher().run(); err != nil {
		t.Fatal(err)
	}
	src := f.read(t, "dev.agentcookie.source")
	if want := []string{"/h/bin/agentcookie", "source", "--watch"}; !slices.Equal(argsOf(t, src), want) {
		t.Errorf("source ProgramArguments = %v, want %v", argsOf(t, src), want)
	}
	if ids, _ := stringList(src["AssociatedBundleIdentifiers"]); !slices.Equal(ids, []string{"com.example.other"}) {
		t.Errorf("source kept AssociatedBundleIdentifiers = %v, want only the other app's", ids)
	}
	sink := f.read(t, "dev.agentcookie.sink")
	if want := []string{"/h/bin/agentcookie", "sink"}; !slices.Equal(argsOf(t, sink), want) {
		t.Errorf("sink ProgramArguments = %v, want %v", argsOf(t, sink), want)
	}
	if _, ok := sink["AssociatedBundleIdentifiers"]; ok {
		t.Error("sink still names agentcookie.app")
	}
	if !slices.Equal(f.ops.bootouts, []string{"dev.agentcookie.source"}) {
		t.Errorf("bootouts = %v; only the loaded source should restart", f.ops.bootouts)
	}
	if !strings.Contains(f.out.String(), "dev.agentcookie.sink: unwrapped") || !strings.Contains(f.out.String(), "not loaded") {
		t.Errorf("output = %q", f.out.String())
	}
}

func TestRefreshSkips(t *testing.T) {
	f := newRefreshFixture(t)
	// Not candidates at all: never read.
	f.write(t, "dev.agentcookie.source.plist.bak", bare("dev.agentcookie.source", "source"))
	f.write(t, "dev.agentcookie.oneshot.1a2b3c4d.plist", bare("dev.agentcookie.oneshot.1a2b3c4d", "wizard"))
	// Candidates that must be skipped.
	f.write(t, "dev.agentcookie.source.plist", bare("dev.agentcookie.other", "source"))
	f.write(t, "dev.agentcookie.cmux-sync.plist", map[string]any{
		"Label": "dev.agentcookie.cmux-sync", "ProgramArguments": []any{"/usr/bin/python3", "x.py"},
	})
	real := f.write(t, "elsewhere.plist", bare("dev.agentcookie.sink", "sink"))
	if err := os.Symlink(real, filepath.Join(f.agents, "dev.agentcookie.sink.plist")); err != nil {
		t.Fatal(err)
	}
	if err := f.refresher().run(); err != nil {
		t.Fatal(err)
	}
	out := f.out.String()
	for _, want := range []string{
		`dev.agentcookie.source: skipped (its Label "dev.agentcookie.other" does not match the file name)`,
		"dev.agentcookie.cmux-sync: skipped (runs /usr/bin/python3, not agentcookie)",
		"dev.agentcookie.sink: skipped (not a regular file)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "oneshot") || strings.Contains(out, ".bak") {
		t.Errorf("refresh looked at a non-candidate:\n%s", out)
	}
	if args := argsOf(t, f.read(t, "dev.agentcookie.oneshot.1a2b3c4d")); args[0] != "/h/bin/agentcookie" {
		t.Errorf("one-shot plist changed: %v", args)
	}
}

func TestRefreshRestoresOriginalWhenBootstrapFails(t *testing.T) {
	f := newRefreshFixture(t)
	p := f.write(t, "dev.agentcookie.source.plist", bare("dev.agentcookie.source", "source", "--watch"))
	orig, _ := os.ReadFile(p)
	f.ops.loaded["dev.agentcookie.source"] = true
	f.ops.failBootstrap = true
	err := f.refresher().run()
	if err == nil {
		t.Fatal("run succeeded although every bootstrap failed")
	}
	got, _ := os.ReadFile(p)
	if !bytes.Equal(got, orig) {
		t.Errorf("original plist not restored:\n%s", got)
	}
	// 3 tries, then one reload of the restored original.
	if len(f.ops.bootstraps) != 4 {
		t.Errorf("bootstraps = %d, want 4", len(f.ops.bootstraps))
	}
	if !strings.Contains(f.out.String(), "restored the original plist") {
		t.Errorf("output = %q", f.out.String())
	}
}

func TestRefreshDoesNotRestartItsOwnJob(t *testing.T) {
	f := newRefreshFixture(t)
	f.write(t, "dev.agentcookie.cmux-sync.plist", bare("dev.agentcookie.cmux-sync", "cmux-sync", "--watch"))
	f.ops.loaded["dev.agentcookie.cmux-sync"] = true
	r := f.refresher()
	r.self = "dev.agentcookie.cmux-sync"
	if err := r.run(); err != nil {
		t.Fatal(err)
	}
	if len(f.ops.bootouts) != 0 {
		t.Errorf("restarted the job refresh runs under: %v", f.ops.bootouts)
	}
	if args := argsOf(t, f.read(t, "dev.agentcookie.cmux-sync")); args[0] != f.launcher {
		t.Errorf("plist not rewritten: %v", args)
	}
	if !strings.Contains(f.out.String(), "takes effect at next restart") {
		t.Errorf("output = %q", f.out.String())
	}
}

func TestRefreshRevertPersistsUntilWrap(t *testing.T) {
	f := newRefreshFixture(t)
	f.write(t, "dev.agentcookie.source.plist", bare("dev.agentcookie.source", "source", "--watch"))
	if err := f.refresher().run(); err != nil {
		t.Fatal(err)
	}
	if args := argsOf(t, f.read(t, "dev.agentcookie.source")); args[0] != f.launcher {
		t.Fatalf("not wrapped: %v", args)
	}

	r := f.refresher()
	r.revert = true
	if err := r.run(); err != nil {
		t.Fatal(err)
	}
	if args := argsOf(t, f.read(t, "dev.agentcookie.source")); args[0] != "/h/bin/agentcookie" {
		t.Fatalf("--revert left it wrapped: %v", args)
	}
	if _, err := os.Stat(launchd.RevertMarker(f.home)); err != nil {
		t.Fatalf("--revert wrote no marker: %v", err)
	}

	// A plain refresh (the installer, the wizard) honors the marker.
	if err := f.refresher().run(); err != nil {
		t.Fatal(err)
	}
	if args := argsOf(t, f.read(t, "dev.agentcookie.source")); args[0] != "/h/bin/agentcookie" {
		t.Fatalf("plain refresh re-wrapped a reverted job: %v", args)
	}

	r = f.refresher()
	r.wrap = true
	if err := r.run(); err != nil {
		t.Fatal(err)
	}
	if args := argsOf(t, f.read(t, "dev.agentcookie.source")); args[0] != f.launcher {
		t.Fatalf("--wrap did not wrap: %v", args)
	}
	if _, err := os.Stat(launchd.RevertMarker(f.home)); !os.IsNotExist(err) {
		t.Fatalf("--wrap left the marker: %v", err)
	}
}

// TestRealPlistEditRoundTrips edits a plist rendered by launchd.Spec with the
// real plutil and checks every original key survives.
func TestRealPlistEditRoundTrips(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("plutil is macOS-only")
	}
	if _, err := exec.LookPath(plutilBin); err != nil {
		t.Skip("no plutil")
	}
	dir := t.TempDir()
	xml, err := launchd.Spec{Role: launchd.RoleSource, BinaryPath: "/h/bin/agentcookie", LogDir: "/h/.agentcookie/logs", ExtraArgs: []string{"--watch"}}.Render()
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "dev.agentcookie.source.plist")
	if err := os.WriteFile(p, xml, 0o644); err != nil {
		t.Fatal(err)
	}
	ops := realServiceOps{}
	before, err := ops.ReadPlist(p)
	if err != nil {
		t.Fatal(err)
	}
	launcher := "/h/Applications/agentcookie.app/Contents/MacOS/agentcookie-launcher"
	if err := ops.EditPlist(p, xml, 0o644, []string{launcher, "/h/bin/agentcookie", "source", "--watch"}, []string{macapp.BundleID}); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(plutilBin, "-lint", p).CombinedOutput(); err != nil {
		t.Fatalf("lint: %s", out)
	}
	after, err := ops.ReadPlist(p)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range before {
		if k == "ProgramArguments" {
			continue
		}
		a, _ := json.Marshal(after[k])
		b, _ := json.Marshal(v)
		if !bytes.Equal(a, b) {
			t.Errorf("key %s changed: %s -> %s", k, b, a)
		}
	}
	if args, _ := stringList(after["ProgramArguments"]); args[0] != launcher {
		t.Errorf("ProgramArguments = %v", args)
	}
	// And back, dropping the key.
	cur, _ := os.ReadFile(p)
	if err := ops.EditPlist(p, cur, 0o644, []string{"/h/bin/agentcookie", "source", "--watch"}, nil); err != nil {
		t.Fatal(err)
	}
	back, _ := ops.ReadPlist(p)
	if _, ok := back["AssociatedBundleIdentifiers"]; ok {
		t.Error("unwrap kept AssociatedBundleIdentifiers")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("temp files left behind: %v", entries)
	}
}
