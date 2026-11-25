package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/mvanhorn/agentcookie/internal/chromebackup"
	"github.com/mvanhorn/agentcookie/internal/chromectl"
	"github.com/mvanhorn/agentcookie/internal/chromepaths"
	"github.com/mvanhorn/agentcookie/internal/config"
)

var (
	chromeBackupFrom       string
	chromeBackupProfiles   []string
	chromeBackupLocalState bool
)

var chromeBackupCmd = &cobra.Command{
	Use:   "chrome-backup",
	Short: "Local snapshots of each Chrome profile's cookies, settings and extension list",
	Long: `chrome-backup keeps snapshots of each Chrome profile on this Mac, in
~/.agentcookie/chrome-backups. Each snapshot holds the profile's cookies,
Preferences, Secure Preferences, bookmarks and autofill, plus Local State
(profile names) and the list of installed extensions. Once a day a snapshot
also holds History and extension storage.

The profile keep-alive takes a snapshot every hour while it runs. When a
profile loses most of its cookies or extensions between two snapshots, it
says so in a notification and keeps the snapshot from before.

To also keep folders Chrome can delete on its own, such as a site's
IndexedDB (Chrome clears site storage when the disk runs low), list them,
relative to the profile folder, in ~/.config/agentcookie/chrome-backup.yaml:

  extra_dirs:
    - IndexedDB/https_example.com_0.indexeddb.leveldb

The daily full snapshot copies them from every profile that has them, and
restore puts them back.

Snapshots never leave this Mac. The last 24 hours are kept, then one a day
for 7 days.`,
}

var chromeBackupNowCmd = &cobra.Command{
	Use:   "now",
	Short: "Take a full snapshot now (with History, extension storage and extra_dirs)",
	RunE: func(cmd *cobra.Command, args []string) error {
		extra, err := chromeBackupExtraDirs()
		if err != nil {
			return err
		}
		s, err := chromebackup.Take(chromepaths.ChromeProfileRoot(), chromebackup.Dir(), time.Now(), true, "manual", extra)
		if err != nil {
			return err
		}
		fmt.Printf("snapshot %s (%s)\n", s.ID, humanBytes(chromebackup.Size(s)))
		printSnapshotProfiles(s)
		return nil
	},
}

var chromeBackupListCmd = &cobra.Command{
	Use:   "list",
	Short: "List snapshots, newest first",
	RunE: func(cmd *cobra.Command, args []string) error {
		snaps, err := chromebackup.List(chromebackup.Dir())
		if err != nil {
			return err
		}
		if len(snaps) == 0 {
			fmt.Println("no snapshots yet; run `agentcookie chrome-backup now`")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tTAKEN\tPROFILES (cookies/extensions)\tSIZE\tNOTE")
		for i, s := range snaps {
			var parts []string
			for _, p := range s.Profiles {
				label := p.Dir
				if p.Name != "" {
					label = p.Name
				}
				parts = append(parts, fmt.Sprintf("%s %d/%d", label, p.Cookies, len(p.Extensions)))
			}
			var note []string
			if s.Heavy {
				note = append(note, "full")
			}
			if s.Keep {
				note = append(note, "kept")
			}
			for _, p := range s.Profiles {
				if len(p.Notes) > 0 || len(p.Errors) > 0 {
					note = append(note, "damaged")
					break
				}
			}
			if s.Reason != "" && s.Reason != "scheduled" {
				note = append(note, s.Reason)
			}
			if i+1 < len(snaps) && len(chromebackup.Warnings(snaps[i+1].Manifest, s.Manifest)) > 0 {
				note = append(note, "WIPE")
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", s.ID, s.Taken.Local().Format("Jan 2 15:04"), strings.Join(parts, ", "), humanBytes(chromebackup.Size(s)), strings.Join(note, " "))
		}
		return w.Flush()
	},
}

var chromeBackupRestoreCmd = &cobra.Command{
	Use:   "restore",
	Short: "Put a snapshot back (quit Chrome first)",
	Long: `Copies a snapshot's files back into Chrome's profile folders. Chrome must
be quit first. Before it writes anything, it takes a full snapshot of the
current state, so a restore can itself be undone.

Without --from it uses the last snapshot before a detected wipe, or the
newest one when no wipe was detected. Without --profile it restores every
profile in the snapshot.

A folder from chrome-backup.yaml's extra_dirs comes back only from a
snapshot that holds it for that profile (see the manifest.json in the
snapshot folder). Undoing a restore puts back folders that existed before
it; a folder the restore created where there was none stays.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		root := chromepaths.ChromeProfileRoot()
		if err := requireChromeQuit(); err != nil {
			return err
		}
		dir := chromebackup.Dir()
		snaps, err := chromebackup.List(dir)
		if err != nil {
			return err
		}
		var pick chromebackup.Snapshot
		if chromeBackupFrom == "" {
			if pick, err = chromebackup.LastGood(snaps); err != nil {
				return err
			}
		} else {
			found := false
			for _, s := range snaps {
				if s.ID == chromeBackupFrom {
					pick, found = s, true
					break
				}
			}
			if !found {
				return fmt.Errorf("chrome-backup restore: no snapshot %q; see `agentcookie chrome-backup list`", chromeBackupFrom)
			}
		}
		for _, p := range pick.Profiles {
			if len(chromeBackupProfiles) > 0 && !slices.Contains(chromeBackupProfiles, p.Dir) {
				continue
			}
			for _, n := range append(append([]string{}, p.Errors...), p.Notes...) {
				fmt.Printf("warning: snapshot %s, %s: %s\n", pick.ID, p.Dir, n)
			}
		}
		// Save the extra dirs too: a restore replaces any the snapshot has.
		extra, err := chromeBackupExtraDirs()
		if err != nil {
			return fmt.Errorf("chrome-backup restore: %w", err)
		}
		safety, err := chromebackup.Take(root, dir, time.Now(), true, "before restore", appendSnapshotDirs(extra, pick))
		if err != nil {
			return fmt.Errorf("chrome-backup restore: could not save the current state first, so nothing was restored: %w", err)
		}
		_ = chromebackup.MarkKeep(safety)
		bad := incompleteFor(safety, pick, chromeBackupProfiles)
		bad = append(bad, uncoveredFolders(pick, safety, chromeBackupProfiles)...)
		if len(bad) > 0 {
			return fmt.Errorf("chrome-backup restore: the current state could not be fully saved, so nothing was restored:\n  %s", strings.Join(bad, "\n  "))
		}
		fmt.Printf("saved the current state as %s\n", safety.ID)
		// Chrome may have been started while the current state was saved.
		if err := requireChromeQuit(); err != nil {
			return err
		}
		done, err := chromebackup.Restore(pick, root, chromeBackupProfiles, chromeBackupLocalState || len(chromeBackupProfiles) == 0)
		for _, f := range done {
			fmt.Printf("restored %s\n", f)
		}
		if err != nil {
			return fmt.Errorf("chrome-backup restore: %w (undo with `agentcookie chrome-backup restore --from %s`)", err, safety.ID)
		}
		fmt.Printf("restored snapshot %s from %s. Open Chrome.\n", pick.ID, pick.Taken.Local().Format("Jan 2 15:04"))
		missing := missingExtensions(pick, root)
		for _, line := range missing {
			fmt.Println(line)
		}
		return nil
	},
}

func init() {
	chromeBackupRestoreCmd.Flags().StringVar(&chromeBackupFrom, "from", "", "snapshot ID to restore (default: the last one before a detected wipe)")
	chromeBackupRestoreCmd.Flags().StringSliceVar(&chromeBackupProfiles, "profile", nil, `profile folder to restore, e.g. "Profile 1" (repeatable; default all)`)
	chromeBackupRestoreCmd.Flags().BoolVar(&chromeBackupLocalState, "local-state", false, "also restore Local State (profile names); always on without --profile")
	chromeBackupCmd.AddCommand(chromeBackupNowCmd, chromeBackupListCmd, chromeBackupRestoreCmd)
}

// chromeBackupExtraDirs reads extra_dirs from chrome-backup.yaml. An entry
// that overlaps what the backup already keeps, or another entry, is an
// error; the dirs accepted so far are still returned.
func chromeBackupExtraDirs() ([]string, error) {
	cfg, err := config.LoadChromeBackup(common.ConfigDir)
	if err != nil {
		return nil, err
	}
	var ok []string
	var bad []string
	for _, d := range cfg.ExtraDirs {
		if err := chromebackup.CheckExtraDir(d, ok); err != nil {
			bad = append(bad, err.Error())
			continue
		}
		ok = append(ok, d)
	}
	if len(bad) > 0 {
		return ok, fmt.Errorf("%s: extra_dirs: %s", config.ChromeBackupPath(common.ConfigDir), strings.Join(bad, "; "))
	}
	return ok, nil
}

// appendSnapshotDirs adds the folders the snapshot being restored holds,
// so the state saved before a restore covers everything it replaces, even
// a folder since dropped from chrome-backup.yaml.
func appendSnapshotDirs(extra []string, s chromebackup.Snapshot) []string {
	out := append([]string{}, extra...)
	for _, p := range s.Profiles {
		for _, f := range p.Files {
			if d, ok := strings.CutSuffix(f, "/"); ok && !slices.Contains(out, d) {
				out = append(out, d)
			}
		}
	}
	return out
}

// uncoveredFolders lists the folders a restore of pick would replace that
// the safety snapshot was not asked to copy, so undo could not put them
// back.
func uncoveredFolders(pick, safety chromebackup.Snapshot, profiles []string) []string {
	saved := append(append([]string{}, chromebackup.HeavyDirs...), safety.ExtraDirs...)
	var out []string
	for _, p := range pick.Profiles {
		if len(profiles) > 0 && !slices.Contains(profiles, p.Dir) {
			continue
		}
		for _, f := range p.Files {
			if d, ok := strings.CutSuffix(f, "/"); ok && !chromebackup.Covered(d, saved) {
				out = append(out, fmt.Sprintf("%s: %s was not saved", p.Dir, d))
			}
		}
	}
	return out
}

// requireChromeQuit fails unless no Chrome browser process exists. It
// checks the process list, not Chrome's lock: a stopped or hung Chrome
// still has the files open.
func requireChromeQuit() error {
	running, err := chromectl.ChromeProcessRunning()
	if err != nil {
		return fmt.Errorf("chrome-backup restore: %w", err)
	}
	if running {
		return errors.New("chrome-backup restore: Chrome is running; quit it with Cmd+Q (or Force Quit if it hangs) and run this again")
	}
	return nil
}

// incompleteFor lists the copy errors in the safety snapshot s that touch
// what restoring pick would replace, for the given profiles (all when
// empty). An error on a file or folder pick leaves alone does not count;
// any other error (the profile folder itself) does.
func incompleteFor(s, pick chromebackup.Snapshot, profiles []string) []string {
	replaced := map[string][]string{}
	for _, p := range pick.Profiles {
		for _, f := range p.Files {
			replaced[p.Dir] = append(replaced[p.Dir], strings.TrimSuffix(f, "/"))
		}
	}
	known := append(append(append([]string{}, chromebackup.LightFiles...), filepath.Join("Network", "Cookies")), chromebackup.HeavyFiles...)
	known = append(append(known, chromebackup.HeavyDirs...), s.ExtraDirs...)
	var out []string
	for _, p := range s.Profiles {
		if len(profiles) > 0 && !slices.Contains(profiles, p.Dir) {
			continue
		}
		for _, e := range p.Errors {
			if path := errorPath(e, known); path != "" && !touches(path, replaced[p.Dir]) {
				continue
			}
			out = append(out, p.Dir+": "+e)
		}
	}
	return out
}

// errorPath returns the known path a snapshot error is about: the longest
// one the error starts with followed by ": ", since a folder name can hold
// ": " itself. It returns "" when none matches.
func errorPath(e string, known []string) string {
	best := ""
	for _, k := range known {
		if strings.HasPrefix(e, k+": ") && len(k) > len(best) {
			best = k
		}
	}
	return best
}

// touches reports whether path is one of paths, inside one, or holds one.
func touches(path string, paths []string) bool {
	path = filepath.Clean(path)
	sep := string(filepath.Separator)
	for _, q := range paths {
		q = filepath.Clean(q)
		if path == q || strings.HasPrefix(path, q+sep) || strings.HasPrefix(q, path+sep) {
			return true
		}
	}
	return false
}

// missingExtensions lists Web Store links for extensions the snapshot had
// that the profile folder no longer holds files for. Chrome reinstalls a
// restored extension on its own only while its files are still there.
func missingExtensions(s chromebackup.Snapshot, root string) []string {
	var out []string
	for _, p := range s.Profiles {
		for _, e := range p.Extensions {
			if _, err := os.Stat(fmt.Sprintf("%s/%s/Extensions/%s", root, p.Dir, e.ID)); err == nil {
				continue
			}
			name := e.Name
			if name == "" || strings.HasPrefix(name, "__MSG_") {
				name = e.ID
			}
			out = append(out, fmt.Sprintf("%s: reinstall %s from https://chromewebstore.google.com/detail/%s", p.Dir, name, e.ID))
		}
	}
	return out
}

func printSnapshotProfiles(s chromebackup.Snapshot) {
	for _, p := range s.Profiles {
		label := p.Dir
		if p.Name != "" {
			label = fmt.Sprintf("%s (%s)", p.Name, p.Dir)
		}
		fmt.Printf("  %s: %d cookies, %d extensions, %d files\n", label, p.Cookies, len(p.Extensions), len(p.Files))
		for _, e := range p.Errors {
			fmt.Printf("    could not copy %s\n", e)
		}
		for _, n := range p.Notes {
			fmt.Printf("    warning: %s\n", n)
		}
	}
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	default:
		return fmt.Sprintf("%d KB", n/1024)
	}
}

// keepaliveBackup is the keep-alive's hourly snapshot step.
func keepaliveBackup(logf func(string, ...any), notifyf func(title, body string)) func(now time.Time) {
	root := chromepaths.ChromeProfileRoot()
	return func(now time.Time) {
		// Read each run so an edit applies without restarting the job. A
		// bad file still lets the regular snapshot run.
		extra, err := chromeBackupExtraDirs()
		if err != nil {
			logf("chrome backup: %v; those extra_dirs skipped", err)
		}
		res, err := chromebackup.RunDue(root, chromebackup.Dir(), now, extra)
		if err != nil {
			logf("chrome backup: %v", err)
			return
		}
		if res.Taken == nil {
			return
		}
		kind := "snapshot"
		if res.Taken.Heavy {
			kind = "full snapshot"
		}
		logf("chrome backup: %s %s taken", kind, res.Taken.ID)
		for _, p := range res.Taken.Profiles {
			for _, e := range p.Errors {
				logf("chrome backup: %s: could not copy %s", p.Dir, e)
			}
			for _, n := range p.Notes {
				logf("chrome backup: %s: warning: %s", p.Dir, n)
			}
		}
		if len(res.Warnings) == 0 {
			return
		}
		for _, w := range res.Warnings {
			logf("chrome backup: %s", w)
		}
		good := ""
		if res.Good != nil {
			good = fmt.Sprintf(" Snapshot %s from %s has the earlier state.", res.Good.ID, res.Good.Taken.Local().Format("15:04"))
		}
		notifyf("agentcookie", res.Warnings[0]+"."+good+" Quit Chrome and run `agentcookie chrome-backup restore` to put it back.")
	}
}
