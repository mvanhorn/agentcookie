package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mvanhorn/agentcookie/internal/launchd"
	"github.com/mvanhorn/agentcookie/internal/macapp"
)

const plutilBin = "/usr/bin/plutil"

var (
	servicesRevert bool
	servicesWrap   bool
)

var servicesCmd = &cobra.Command{
	Use:   "services",
	Short: "Manage agentcookie's macOS LaunchAgents",
}

var servicesRefreshCmd = &cobra.Command{
	Use:   "refresh",
	Short: `Make agentcookie's LaunchAgents show as "agentcookie" in Login Items`,
	Long: `refresh makes the agentcookie LaunchAgents in ~/Library/LaunchAgents
start through ~/Applications/agentcookie.app, so System Settings > General >
Login Items lists them as "agentcookie" with its icon instead of under the
signing certificate's personal name. A release build installs or updates
the app first.

Each plist is edited in place with plutil: the launcher goes first in
ProgramArguments, ahead of the agentcookie binary, and
AssociatedBundleIdentifiers names the app. Every other key is kept. A job
is wrapped only when the app passes its signature check and the binary the
job runs is a release-signed agentcookie; otherwise it is set back to run
the binary directly. The sink always runs directly, because it quits Chrome
through an Automation grant that must stay agentcookie's own. Loaded jobs
are restarted, except the one this command runs under.

--revert sets every job back to run agentcookie directly and keeps it that
way: later refreshes, the wizard and cmux-sync enable leave jobs bare until
you run --wrap.

Do not delete ~/Applications/agentcookie.app while jobs start through it;
run refresh --revert first, or uninstall with agentcookie wizard uninstall.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if servicesRevert && servicesWrap {
			return fmt.Errorf("--revert and --wrap cannot be used together")
		}
		if runtime.GOOS != "darwin" {
			fmt.Fprintf(cmd.OutOrStdout(), "agentcookie services: nothing to refresh on %s; only macOS lists LaunchAgents in Login Items\n", runtime.GOOS)
			return nil
		}
		r, err := newServiceRefresher(cmd.OutOrStdout())
		if err != nil {
			return err
		}
		r.revert, r.wrap = servicesRevert, servicesWrap
		return r.run()
	},
}

func init() {
	servicesRefreshCmd.Flags().BoolVar(&servicesRevert, "revert", false, "run every agentcookie LaunchAgent directly, without agentcookie.app, and keep it that way")
	servicesRefreshCmd.Flags().BoolVar(&servicesWrap, "wrap", false, "undo --revert: start LaunchAgents through agentcookie.app again")
	servicesCmd.AddCommand(servicesRefreshCmd)
}

// serviceOps is the plutil and launchctl work refresh does, so tests can run
// without either.
type serviceOps interface {
	// ReadPlist returns the plist at path as JSON-shaped values.
	ReadPlist(path string) (map[string]any, error)
	// EditPlist sets ProgramArguments and AssociatedBundleIdentifiers (nil
	// removes the key) on a copy of orig and renames it over path.
	EditPlist(path string, orig []byte, mode os.FileMode, args, ids []string) error
	Loaded(label string) bool
	Bootout(label string) error
	Bootstrap(path string) error
}

// serviceRefresher moves agentcookie's LaunchAgents onto or off the
// agentcookie.app launcher, following launchd.LauncherFor.
type serviceRefresher struct {
	home   string
	revert bool
	wrap   bool
	// launcherFor is the wrap decision for one job.
	launcherFor func(role launchd.Role, target string) string
	ops         serviceOps
	self        string // the launchd job this process runs under, if any
	sleep       func()
	out         io.Writer
}

func newServiceRefresher(out io.Writer) (*serviceRefresher, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return &serviceRefresher{
		home:        home,
		launcherFor: launcherFor,
		ops:         realServiceOps{uid: os.Getuid()},
		self:        os.Getenv("XPC_SERVICE_NAME"),
		sleep:       func() { time.Sleep(500 * time.Millisecond) },
		out:         out,
	}, nil
}

// refreshServices brings the other agentcookie LaunchAgents in line after an
// install, so a re-run of the wizard or cmux-sync enable fixes jobs it did not
// rewrite. A failure is reported, not returned: the install itself worked.
func refreshServices() {
	if runtime.GOOS != "darwin" {
		return
	}
	r, err := newServiceRefresher(os.Stderr)
	if err != nil {
		return
	}
	r.out = prefixWriter{w: os.Stderr, prefix: "agentcookie services: "}
	if err := r.run(); err != nil {
		fmt.Fprintf(os.Stderr, "agentcookie services: %v (run `agentcookie services refresh` to retry)\n", err)
	}
}

func (r *serviceRefresher) run() error {
	marker := launchd.RevertMarker(r.home)
	switch {
	case r.revert:
		if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(marker, []byte("written by `agentcookie services refresh --revert`; remove with --wrap\n"), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", marker, err)
		}
	case r.wrap:
		if err := os.Remove(marker); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", marker, err)
		}
	}
	var failed []string
	for _, role := range managedRoles {
		label := launchd.Spec{Role: role}.Label()
		path := launchd.Spec{Role: role}.PathIn(r.home)
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			continue
		}
		msg, err := r.refreshOne(path, label, role)
		if err != nil {
			failed = append(failed, label)
			msg = err.Error()
		}
		fmt.Fprintf(r.out, "%s: %s\n", label, msg)
	}
	if len(failed) > 0 {
		return fmt.Errorf("could not refresh %s", strings.Join(failed, ", "))
	}
	return nil
}

// refreshOne brings one plist to the form the wrap decision wants and
// returns a short status. Skipped plists are not errors.
func (r *serviceRefresher) refreshOne(path, label string, role launchd.Role) (string, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "skipped (not a regular file)", nil
	}
	m, err := r.ops.ReadPlist(path)
	if err != nil {
		return "", fmt.Errorf("read failed: %w", err)
	}
	if l, _ := m["Label"].(string); l != label {
		return fmt.Sprintf("skipped (its Label %q does not match the file name)", l), nil
	}
	if _, ok := m["Program"]; ok {
		return "skipped (uses the Program key; agentcookie never writes one)", nil
	}
	args, ok := stringList(m["ProgramArguments"])
	if !ok || len(args) == 0 {
		return "skipped (no ProgramArguments)", nil
	}
	cur, rest := "", args
	if macapp.IsLauncher(args[0]) {
		if len(args) < 2 {
			return "skipped (launcher with no program after it)", nil
		}
		cur, rest = args[0], args[1:]
	}
	target := rest[0]
	if filepath.Base(target) != "agentcookie" {
		return fmt.Sprintf("skipped (runs %s, not agentcookie)", target), nil
	}

	want := r.launcherFor(role, target)
	ids, _ := stringList(m["AssociatedBundleIdentifiers"])
	wantIDs := slices.DeleteFunc(slices.Clone(ids), func(s string) bool { return s == macapp.BundleID })
	wantArgs := slices.Clone(rest)
	status := "unwrapped (runs agentcookie directly)"
	if want != "" {
		wantArgs = append([]string{want}, rest...)
		wantIDs = append(wantIDs, macapp.BundleID)
		status = `wrapped (shows as "agentcookie" in Login Items)`
	}
	if slices.Equal(args, wantArgs) && slices.Equal(ids, wantIDs) {
		switch {
		case want != "":
			return "unchanged", nil
		case role == launchd.RoleSink:
			return "unchanged (the sink runs agentcookie directly)", nil
		case launchd.Reverted(r.home):
			return "unchanged (reverted; `agentcookie services refresh --wrap` wraps it)", nil
		}
		return "unchanged (" + r.bareReason(target) + ")", nil
	}
	if cur == "" && want == "" {
		status = "unchanged program; removed a stale AssociatedBundleIdentifiers entry"
	}
	orig, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if err := r.ops.EditPlist(path, orig, fi.Mode().Perm(), wantArgs, wantIDs); err != nil {
		return "", fmt.Errorf("update failed: %w", err)
	}
	return r.apply(path, label, fi.Mode().Perm(), orig, status)
}

// bareReason says why a non-sink job was left running agentcookie directly.
func (r *serviceRefresher) bareReason(target string) string {
	if _, err := os.Stat(macapp.Path(r.home)); err != nil {
		return "agentcookie.app is not installed; a release build of agentcookie installs it"
	}
	return "agentcookie.app or " + target + " fails its signature check"
}

// apply restarts a loaded job after its plist changed, putting orig back
// when the restart keeps failing.
func (r *serviceRefresher) apply(path, label string, mode os.FileMode, orig []byte, status string) (string, error) {
	if !r.ops.Loaded(label) {
		return status + "; not loaded, so it takes effect when it next starts", nil
	}
	if label == r.self {
		return status + "; takes effect at next restart (refresh is running under this job)", nil
	}
	if err := r.restart(label, path); err != nil {
		werr := writeFileAtomic(path, orig, mode)
		var berr error
		if werr == nil && !r.ops.Loaded(label) {
			berr = r.ops.Bootstrap(path)
		}
		return "", fmt.Errorf("restart failed (%v); restored the original plist%s", err, restoreNote(werr, berr))
	}
	return status + ", restarted", nil
}

func restoreNote(werr, berr error) string {
	switch {
	case werr != nil:
		return fmt.Sprintf(", but writing it back failed: %v", werr)
	case berr != nil:
		return fmt.Sprintf(", but loading it failed: %v", berr)
	}
	return " and reloaded it"
}

// restart boots the job out, waits for launchd to let it go, and bootstraps
// it again. kickstart would not reread the plist. A bootstrap right after
// bootout can fail while the old instance is torn down, so it retries.
func (r *serviceRefresher) restart(label, path string) error {
	if err := r.ops.Bootout(label); err != nil {
		return err
	}
	for i := 0; i < 20 && r.ops.Loaded(label); i++ {
		r.sleep()
	}
	var err error
	for range 3 {
		if err = r.ops.Bootstrap(path); err == nil {
			return nil
		}
		r.sleep()
	}
	return err
}

func stringList(v any) ([]string, bool) {
	xs, ok := v.([]any)
	if !ok {
		return nil, false
	}
	s := make([]string, 0, len(xs))
	for _, x := range xs {
		str, ok := x.(string)
		if !ok {
			return nil, false
		}
		s = append(s, str)
	}
	return s, true
}

// realServiceOps runs plutil and launchctl.
type realServiceOps struct{ uid int }

func (o realServiceOps) target(label string) string { return fmt.Sprintf("gui/%d/%s", o.uid, label) }

func (o realServiceOps) ReadPlist(path string) (map[string]any, error) {
	out, err := exec.Command(plutilBin, "-convert", "json", "-o", "-", path).Output()
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func (o realServiceOps) EditPlist(path string, orig []byte, mode os.FileMode, args, ids []string) error {
	tmp, err := stageTemp(path, orig, mode)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	argsJSON, _ := json.Marshal(args)
	edits := [][]string{{"-replace", "ProgramArguments", "-json", string(argsJSON)}}
	if len(ids) == 0 {
		edits = append(edits, []string{"-remove", "AssociatedBundleIdentifiers"})
	} else {
		idsJSON, _ := json.Marshal(ids)
		edits = append(edits, []string{"-replace", "AssociatedBundleIdentifiers", "-json", string(idsJSON)})
	}
	for _, e := range edits {
		out, err := exec.Command(plutilBin, append(e, tmp)...).CombinedOutput()
		if err != nil && (e[0] != "-remove" || !strings.Contains(string(out), "No value to remove")) {
			return fmt.Errorf("plutil %s: %v: %s", strings.Join(e[:2], " "), err, strings.TrimSpace(string(out)))
		}
	}
	if out, err := exec.Command(plutilBin, "-lint", tmp).CombinedOutput(); err != nil {
		return fmt.Errorf("edited plist is invalid: %s", strings.TrimSpace(string(out)))
	}
	return os.Rename(tmp, path)
}

func (o realServiceOps) Loaded(label string) bool {
	return exec.Command("/bin/launchctl", "print", o.target(label)).Run() == nil
}

func (o realServiceOps) Bootout(label string) error {
	if out, err := exec.Command("/bin/launchctl", "bootout", o.target(label)).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl bootout: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (o realServiceOps) Bootstrap(path string) error {
	if out, err := exec.Command("/bin/launchctl", "bootstrap", fmt.Sprintf("gui/%d", o.uid), path).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl bootstrap: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// stageTemp writes data with mode to a new temp file next to path. The
// name does not end in .plist, so launchd never picks it up.
func stageTemp(path string, data []byte, mode os.FileMode) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(path), ".agentcookie-refresh-*")
	if err != nil {
		return "", err
	}
	_, werr := f.Write(data)
	cerr := f.Close()
	if err := errors.Join(werr, cerr, os.Chmod(f.Name(), mode)); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// prefixWriter prefixes each Write, which refresh makes once per line.
type prefixWriter struct {
	w      io.Writer
	prefix string
}

func (p prefixWriter) Write(b []byte) (int, error) {
	if _, err := io.WriteString(p.w, p.prefix); err != nil {
		return 0, err
	}
	return p.w.Write(b)
}
