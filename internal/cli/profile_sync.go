package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/mvanhorn/agentcookie/internal/chrome"
)

var (
	profileSyncFrom    string
	profileSyncTo      string
	profileSyncBrowser string
	profileSyncDryRun  bool
	profileSyncExclude []string
	profileSyncVerbose bool
)

// profileSyncPasswordFor is the Keychain reader seam; tests stub it.
var profileSyncPasswordFor = chrome.SafeStoragePasswordFor

// profileSyncDefaultExcludes are registrable domains never copied between
// profiles. Each profile keeps its own Claude login: the whole point of a
// second profile is usually a second Claude account for Claude in Chrome, and
// copying claude.ai cookies would sign it into the first account. Google
// account cookies are device-bound (DBSC) and are dropped by the DBSC filter.
var profileSyncDefaultExcludes = []string{"claude.ai", "claude.com", "anthropic.com"}

var profileSyncCmd = &cobra.Command{
	Use:   "profile-sync",
	Short: "Copy this Mac's logins from one Chrome profile into another local profile, one-way",
	Long: `profile-sync copies cookies from one Chrome profile on this Mac into
another profile of the same Chrome, so a second profile (for example one
running Claude in Chrome on a second Claude account) is logged into the same
sites as your main one.

It is one-way and never clobbers the target's own logins:

  - a cookie the target does not have is copied;
  - a cookie profile-sync copied earlier, and the target has not changed
    since, is updated when the source changes;
  - a cookie the target set or changed itself is left alone.

claude.ai, claude.com and anthropic.com are never copied, so each profile
keeps its own Claude account. Device-bound Google account cookies (DBSC) are
skipped; sign into Google in each profile directly.

Chrome only reads a profile's cookie store when the profile loads, so the
target profile must be closed: close its windows first (profile-sync refuses
while any process still has the target's cookie store open), then run:

  agentcookie profile-sync --to "Profile 1"
  agentcookie profile-sync --to "Profile 1" --dry-run    # show the plan only

The source profile can stay open; it is only read.`,
	RunE: runProfileSync,
}

func init() {
	profileSyncCmd.Flags().StringVar(&profileSyncFrom, "from", "Default", "source profile directory name")
	profileSyncCmd.Flags().StringVar(&profileSyncTo, "to", "", `target profile directory name, e.g. "Profile 1" (required)`)
	profileSyncCmd.Flags().StringVar(&profileSyncBrowser, "browser", "", "browser name (default: Chrome)")
	profileSyncCmd.Flags().BoolVar(&profileSyncDryRun, "dry-run", false, "print what would be copied without writing")
	profileSyncCmd.Flags().StringSliceVar(&profileSyncExclude, "exclude", nil, "extra registrable domains to never copy (repeatable), e.g. --exclude chase.com")
	profileSyncCmd.Flags().BoolVar(&profileSyncVerbose, "verbose", false, "list skipped target-owned cookies by host")
}

var profileDirNameRE = regexp.MustCompile(`^(Default|Profile [0-9]+)$`)

func runProfileSync(cmd *cobra.Command, args []string) error {
	if profileSyncTo == "" {
		return fmt.Errorf(`--to is required, e.g. --to "Profile 1"`)
	}
	for _, p := range []string{profileSyncFrom, profileSyncTo} {
		if !profileDirNameRE.MatchString(p) {
			return fmt.Errorf("profile %q is not a Chrome profile directory name (Default or \"Profile N\")", p)
		}
	}
	if profileSyncFrom == profileSyncTo {
		return fmt.Errorf("--from and --to are the same profile (%s)", profileSyncTo)
	}

	browser, err := chrome.LookupBrowser(profileSyncBrowser)
	if err != nil {
		return err
	}
	srcPath := browser.CookiesPath(profileSyncFrom)
	dstPath := browser.CookiesPath(profileSyncTo)
	if _, err := os.Stat(dstPath); err != nil {
		return fmt.Errorf("target profile %q has no cookie store at %s (open it in Chrome once first): %w", profileSyncTo, dstPath, err)
	}

	blocklist, err := loadFreshBlocklist()
	if err != nil {
		return err
	}
	password, err := profileSyncPasswordFor(browser)
	if err != nil {
		return err
	}
	key, err := chrome.DeriveAESKey(password)
	if err != nil {
		return err
	}

	statePath := profileSyncStatePath(browser.Name, profileSyncTo)
	// Hold a per-target lock from the target/state read through the state
	// save, so overlapping runs cannot interleave writes and ownership.
	unlock, err := profileSyncLock(statePath + ".lock")
	if err != nil {
		return err
	}
	defer unlock()

	// Chrome keeps no SQLite lock on Cookies, and a loaded profile would never
	// pick up rows written under it (and could later overwrite them). A real
	// run refuses unless no process holds the target's cookie store open.
	open, err := profileSyncFileInUse(dstPath)
	if err != nil {
		return fmt.Errorf("check whether profile %q is open: %w", profileSyncTo, err)
	}
	if open && !profileSyncDryRun {
		return fmt.Errorf("profile %q is open in Chrome; close every window of that profile (Chrome may take a few seconds to let go) and run again", profileSyncTo)
	}

	// skipDBSC=true: device-bound cookies cannot move between profiles.
	src, st, err := readFilteredCookies(srcPath, blocklist, key, true, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("read source profile %q: %w", profileSyncFrom, err)
	}
	// A closed target is read WAL-aware, so committed rows Chrome left in
	// Cookies-wal count as the target's own. An open one (dry-run only) is
	// read as a snapshot.
	readTarget := chrome.ReadCookiesForHostClosed
	if open {
		readTarget = chrome.ReadCookiesForHost
	}
	dst, err := readTarget(dstPath, "%", key)
	if err != nil {
		return fmt.Errorf("read target profile %q: %w", profileSyncTo, err)
	}

	state, err := loadProfileSyncState(statePath)
	if err != nil {
		return err
	}

	excludes := append(append([]string(nil), profileSyncDefaultExcludes...), profileSyncExclude...)
	plan := planProfileSync(src, dst, state, excludes)

	out := cmd.ErrOrStderr()
	fmt.Fprintf(out, "agentcookie profile-sync: %s -> %s: source %d (policy dropped %d, device-bound skipped %d), copy %d new, update %d, unchanged %d, target-owned kept %d, excluded %d\n",
		profileSyncFrom, profileSyncTo, st.totalRead, st.totalDropped, st.dbsc.skipped,
		plan.added, plan.updated, plan.unchanged, len(plan.targetOwned), plan.excluded)
	if profileSyncVerbose {
		for _, h := range summarizeHosts(plan.targetOwned) {
			fmt.Fprintf(out, "  kept target's own: %s\n", h)
		}
	}
	if profileSyncDryRun || len(plan.write) == 0 {
		if profileSyncDryRun {
			fmt.Fprintln(out, "agentcookie profile-sync: dry-run; nothing written")
		}
		return nil
	}

	n, err := chrome.WriteCookiesChromeNative(dstPath, plan.write, key)
	if err != nil {
		return fmt.Errorf("write target profile %q: %w", profileSyncTo, err)
	}
	for _, c := range plan.write {
		state.Written[profileSyncKey(c)] = profileSyncValueHash(c)
	}
	if err := saveProfileSyncState(statePath, state); err != nil {
		return err
	}
	fmt.Fprintf(out, "agentcookie profile-sync: wrote %d cookies into %s\n", n, profileSyncTo)
	return nil
}

// profileSyncFileInUse reports whether any process has path open, via lsof.
// lsof exits 1 with no output when nothing has the file open. A seam so tests
// do not depend on lsof or a running Chrome.
var profileSyncFileInUse = func(path string) (bool, error) {
	out, err := exec.Command("lsof", "-t", "--", path).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 && len(out) == 0 {
			return false, nil
		}
		return false, err
	}
	return len(strings.TrimSpace(string(out))) > 0, nil
}

// profileSyncLock takes an exclusive flock on path, failing fast when another
// profile-sync for the same target holds it.
func profileSyncLock(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create profile-sync state dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open profile-sync lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another profile-sync for this profile is running (%s): %w", path, err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// profileSyncState records, per copied cookie, a hash of the value
// profile-sync last wrote. A target cookie whose current value still matches
// is "ours" and may be updated; anything else belongs to the target.
type profileSyncState struct {
	Written map[string]uint64 `json:"written"`
}

type profileSyncPlan struct {
	write       []chrome.Cookie
	targetOwned []chrome.Cookie
	added       int
	updated     int
	unchanged   int
	excluded    int
}

// planProfileSync decides which source cookies to write into the target.
func planProfileSync(src, dst []chrome.Cookie, state *profileSyncState, excludes []string) profileSyncPlan {
	current := make(map[string]chrome.Cookie, len(dst))
	for _, c := range dst {
		current[profileSyncKey(c)] = c
	}
	var p profileSyncPlan
	for _, c := range src {
		if hostInDomains(c.HostKey, excludes) {
			p.excluded++
			continue
		}
		k := profileSyncKey(c)
		have, ok := current[k]
		switch {
		case !ok:
			p.write = append(p.write, c)
			p.added++
		case profileSyncValueHash(have) == profileSyncValueHash(c):
			p.unchanged++
		case state.Written[k] == profileSyncValueHash(have):
			p.write = append(p.write, c)
			p.updated++
		default:
			p.targetOwned = append(p.targetOwned, have)
		}
	}
	return p
}

// profileSyncKey is Chrome's full unique cookie key, so partitioned (CHIPS)
// cookies and scheme/port variants of the same name stay distinct rows through
// planning, state, and the upsert.
func profileSyncKey(c chrome.Cookie) string {
	return fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%d\x00%d\x00%d",
		c.HostKey, c.TopFrameSiteKey, c.Name, c.Path, c.HasCrossSiteAncestor, c.SourceScheme, c.SourcePort)
}

// profileSyncValueHash is the content identity used for ownership: the same
// digest cmux-sync uses for change detection.
func profileSyncValueHash(c chrome.Cookie) uint64 { return cmuxCookieHash(c) }

func hostInDomains(host string, domains []string) bool {
	host = strings.ToLower(strings.TrimPrefix(host, "."))
	for _, d := range domains {
		d = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(d), "."))
		if d != "" && (host == d || strings.HasSuffix(host, "."+d)) {
			return true
		}
	}
	return false
}

func summarizeHosts(cs []chrome.Cookie) []string {
	seen := map[string]int{}
	var order []string
	for _, c := range cs {
		h := strings.TrimPrefix(c.HostKey, ".")
		if seen[h] == 0 {
			order = append(order, h)
		}
		seen[h]++
	}
	out := make([]string, len(order))
	for i, h := range order {
		out[i] = fmt.Sprintf("%s (%d)", h, seen[h])
	}
	return out
}

func profileSyncStatePath(browser, profile string) string {
	home, _ := os.UserHomeDir()
	name := strings.ToLower(browser) + "-" + strings.ReplaceAll(strings.ToLower(profile), " ", "-") + ".json"
	return filepath.Join(home, ".agentcookie", "profile-sync", name)
}

func loadProfileSyncState(path string) (*profileSyncState, error) {
	st := &profileSyncState{Written: map[string]uint64{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read profile-sync state: %w", err)
	}
	if err := json.Unmarshal(b, st); err != nil {
		return nil, fmt.Errorf("parse profile-sync state %s: %w", path, err)
	}
	if st.Written == nil {
		st.Written = map[string]uint64{}
	}
	return st, nil
}

// saveProfileSyncState stores only value hashes (never cookie values), 0600,
// written atomically.
func saveProfileSyncState(path string, st *profileSyncState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create profile-sync state dir: %w", err)
	}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create profile-sync state temp: %w", err)
	}
	tmp := f.Name()
	_, werr := f.Write(b)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmp)
		return fmt.Errorf("write profile-sync state: %v", errors.Join(werr, cerr))
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("publish profile-sync state: %w", err)
	}
	return nil
}
