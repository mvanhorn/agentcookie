// Package chromebackup keeps local snapshots of each Chrome profile's
// cookies, settings and extension list, so a profile Chrome wiped (a reset
// Preferences file, a lost cookie store) can be put back in one step.
//
// A snapshot is a folder under Dir named by its UTC time. It holds a gzip
// copy of each backed-up file, the extension list of each profile, and a
// manifest with counts used to spot a wipe. Snapshots never leave the Mac.
package chromebackup

import (
	"compress/gzip"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3" // registers the sqlite3 driver

	"github.com/mvanhorn/agentcookie/internal/chromepaths"
)

// idLayout names a snapshot folder; it sorts by time.
const idLayout = "20060102T150405Z"

// manifestName is the manifest file inside a snapshot folder.
const manifestName = "manifest.json"

// LightFiles are copied into every snapshot: small, and what a wipe takes.
var LightFiles = []string{"Cookies", "Preferences", "Secure Preferences", "Bookmarks", "Web Data"}

// HeavyFiles and HeavyDirs are copied into at most one snapshot a day.
var (
	HeavyFiles = []string{"History"}
	HeavyDirs  = []string{"Local Extension Settings"}
)

// sqliteFiles are SQLite databases, by base name: copied with their -wal
// and compacted so the snapshot holds every committed row.
var sqliteFiles = map[string]bool{"Cookies": true, "Web Data": true, "History": true}

var profileDirRE = regexp.MustCompile(`^(Default|Profile \d+)$`)

// Extension is one installed extension from a profile's Preferences.
type Extension struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	Enabled bool   `json:"enabled"`
}

// Profile is what a snapshot recorded about one profile.
type Profile struct {
	Dir  string `json:"dir"`
	Name string `json:"name,omitempty"`
	// Cookies is the cookie count: 0 when the profile has no cookie
	// database, -1 when it could not be read.
	Cookies    int         `json:"cookies"`
	Extensions []Extension `json:"extensions"`
	// Files are paths relative to the profile folder; a trailing "/" marks
	// a folder.
	Files  []string `json:"files"`
	Errors []string `json:"errors,omitempty"`
	// Notes are problems that did not stop a file being copied: a database
	// that failed its integrity check (stored as-is with its -wal), or
	// counts that could not be read from a damaged file.
	Notes []string `json:"notes,omitempty"`
}

// Manifest describes one snapshot.
type Manifest struct {
	ID     string    `json:"id"`
	Taken  time.Time `json:"taken"`
	Heavy  bool      `json:"heavy"`
	Reason string    `json:"reason,omitempty"`
	Keep   bool      `json:"keep,omitempty"`
	// ExtraDirs are the owner's extra profile folders (chrome-backup.yaml)
	// a heavy snapshot was asked to copy. Each profile's Files lists the
	// ones it had.
	ExtraDirs []string  `json:"extra_dirs,omitempty"`
	Profiles  []Profile `json:"profiles"`
}

// Snapshot is a manifest and the folder it came from.
type Snapshot struct {
	Manifest
	Path string
}

// Dir is where snapshots live.
func Dir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".agentcookie", "chrome-backups")
}

// Take writes a snapshot of every profile under root into dir. heavy adds
// HeavyFiles, HeavyDirs and extraDirs (folders relative to each profile
// folder, such as one site's IndexedDB; a light snapshot ignores them). A file that fails to copy is recorded in the
// profile's Errors and does not stop the others. A root with no profiles
// still gets a snapshot, so a vanished profile shows up as a wipe and a
// restore can still save the current state first.
//
// Snapshots are serialized across processes with a lock file in dir, and
// each one stages in its own folder.
func Take(root, dir string, now time.Time, heavy bool, reason string, extraDirs []string) (Snapshot, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Snapshot{}, fmt.Errorf("chromebackup: %w", err)
	}
	unlock, err := lockDir(dir)
	if err != nil {
		return Snapshot{}, err
	}
	defer unlock()
	profiles, err := profileDirs(root)
	if err != nil {
		return Snapshot{}, err
	}
	names := profileNames(root)
	// A snapshot already taken this second (a restore right after the
	// scheduled one) moves this one to the next free second.
	at := now.UTC().Truncate(time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, at.Format(idLayout))); errors.Is(err, fs.ErrNotExist) {
			break
		}
		at = at.Add(time.Second)
	}
	id := at.Format(idLayout)
	final := filepath.Join(dir, id)
	tmp, err := os.MkdirTemp(dir, id+".*.partial")
	if err != nil {
		return Snapshot{}, fmt.Errorf("chromebackup: %w", err)
	}
	m := Manifest{ID: id, Taken: at, Heavy: heavy, Reason: reason, Profiles: []Profile{}}
	dirs := []string{}
	if heavy {
		dirs = heavyDirs(extraDirs)
		m.ExtraDirs = dirs[len(HeavyDirs):]
	}
	if err := gzipFile(filepath.Join(root, "Local State"), filepath.Join(tmp, "Local State.gz")); err != nil && !errors.Is(err, fs.ErrNotExist) {
		_ = os.RemoveAll(tmp)
		return Snapshot{}, fmt.Errorf("chromebackup: Local State: %w", err)
	}
	for _, p := range profiles {
		m.Profiles = append(m.Profiles, snapshotProfile(root, tmp, p, names[p], heavy, dirs))
	}
	if err := writeManifest(tmp, m); err != nil {
		_ = os.RemoveAll(tmp)
		return Snapshot{}, err
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.RemoveAll(tmp)
		return Snapshot{}, fmt.Errorf("chromebackup: %w", err)
	}
	return Snapshot{Manifest: m, Path: final}, nil
}

// heavyDirs is HeavyDirs followed by the extra dirs CheckExtraDir accepts.
// Wider folders go first, and a folder inside one already taken is left
// out because it is copied with it, so asking for both IndexedDB and one
// site's IndexedDB copies all of IndexedDB.
func heavyDirs(extra []string) []string {
	cands := make([]string, 0, len(extra))
	for _, d := range extra {
		cands = append(cands, filepath.Clean(d))
	}
	sep := string(filepath.Separator)
	sort.SliceStable(cands, func(i, j int) bool {
		return strings.Count(cands[i], sep) < strings.Count(cands[j], sep)
	})
	out := append([]string{}, HeavyDirs...)
	for _, d := range cands {
		if Covered(d, out) || CheckExtraDir(d, out[len(HeavyDirs):]) != nil {
			continue
		}
		out = append(out, d)
	}
	return out
}

// Covered reports whether folder d is one of dirs or inside one of them.
func Covered(d string, dirs []string) bool {
	d = filepath.Clean(d)
	for _, c := range dirs {
		if d == c || strings.HasPrefix(d, c+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// builtinPaths are the profile paths every snapshot already keeps.
func builtinPaths() []string {
	out := append([]string{}, LightFiles...)
	out = append(out, filepath.Join("Network", "Cookies"))
	out = append(out, HeavyFiles...)
	return append(out, HeavyDirs...)
}

// CheckExtraDir reports why d, a folder relative to the profile folder,
// cannot be an extra dir: it leaves the profile folder, or it holds, sits
// inside or equals a path a snapshot already keeps or one of others (the
// extra dirs accepted before it). A folder already in HeavyDirs is fine;
// it is copied once.
func CheckExtraDir(d string, others []string) error {
	d = filepath.Clean(d)
	if d == "." || filepath.IsAbs(d) || d == ".." || strings.HasPrefix(d, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%q is not inside the profile folder", d)
	}
	if slices.Contains(HeavyDirs, d) {
		return nil
	}
	for _, b := range append(builtinPaths(), others...) {
		if overlaps(d, b) {
			return fmt.Errorf("%q overlaps %q, which the backup already keeps", d, b)
		}
	}
	return nil
}

// overlaps reports whether a and b are the same path or one holds the other.
func overlaps(a, b string) bool {
	sep := string(filepath.Separator)
	return a == b || strings.HasPrefix(b, a+sep) || strings.HasPrefix(a, b+sep)
}

// noSymlinks fails when a folder on the way from base to base/rel,
// including rel itself, is a symlink, so a copy or restore never leaves the
// profile folder. A missing component ends the check.
func noSymlinks(base, rel string) error {
	cur := base
	for part := range strings.SplitSeq(filepath.Clean(rel), string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink; not followed", cur)
		}
	}
	return nil
}

func snapshotProfile(root, snap, dir, name string, heavy bool, dirs []string) Profile {
	p := Profile{Dir: dir, Name: name, Cookies: -1}
	src := filepath.Join(root, dir)
	dst := filepath.Join(snap, dir)
	if err := os.MkdirAll(dst, 0o700); err != nil {
		p.Errors = append(p.Errors, err.Error())
		return p
	}
	// Newer Chrome keeps cookies in Network/Cookies.
	cookiesRel, _ := filepath.Rel(src, chromepaths.ProfileCookiesDB(src))
	files := []string{}
	for _, f := range LightFiles {
		if f == "Cookies" {
			f = cookiesRel
		}
		files = append(files, f)
	}
	if heavy {
		files = append(files, HeavyFiles...)
	}
	for _, f := range files {
		in, out := filepath.Join(src, f), filepath.Join(dst, f+".gz")
		if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
			p.Errors = append(p.Errors, fmt.Sprintf("%s: %v", f, err))
			continue
		}
		var err error
		if sqliteFiles[filepath.Base(f)] {
			var raw bool
			raw, err = copySQLite(in, out)
			if err == nil && raw {
				p.Notes = append(p.Notes, f+": failed its integrity check; stored as-is")
				if _, statErr := os.Stat(filepath.Join(dst, f+"-wal.gz")); statErr == nil {
					p.Files = append(p.Files, f, f+"-wal")
					continue
				}
			}
		} else {
			err = gzipFile(in, out)
		}
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			p.Errors = append(p.Errors, fmt.Sprintf("%s: %v", f, err))
		default:
			p.Files = append(p.Files, f)
		}
	}
	if heavy {
		for _, d := range dirs {
			err := noSymlinks(src, d)
			if err == nil {
				err = copyTreeGzip(filepath.Join(src, d), filepath.Join(dst, d))
			}
			switch {
			case errors.Is(err, fs.ErrNotExist):
			case err != nil:
				p.Errors = append(p.Errors, fmt.Sprintf("%s: %v", d, err))
			default:
				p.Files = append(p.Files, d+"/")
			}
		}
	}
	switch n, err := countCookies(filepath.Join(src, cookiesRel)); {
	case err == nil:
		p.Cookies = n
	case errors.Is(err, fs.ErrNotExist):
		p.Cookies = 0 // no cookie database at all counts as none
	default:
		p.Notes = append(p.Notes, fmt.Sprintf("count cookies: %v", err))
	}
	exts, err := Extensions(filepath.Join(src, "Preferences"), filepath.Join(src, "Secure Preferences"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		p.Notes = append(p.Notes, fmt.Sprintf("extensions: %v", err))
	}
	p.Extensions = exts
	if p.Extensions == nil {
		p.Extensions = []Extension{}
	}
	return p
}

// profileDirs lists the profile folders under root that have Preferences or
// a cookie database.
func profileDirs(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("chromebackup: %w", err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() || !profileDirRE.MatchString(e.Name()) {
			continue
		}
		p := filepath.Join(root, e.Name())
		for _, f := range []string{filepath.Join(p, "Preferences"), chromepaths.ProfileCookiesDB(p)} {
			if _, err := os.Stat(f); err == nil {
				out = append(out, e.Name())
				break
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// profileNames maps profile folders to the names Chrome shows, from Local
// State. A missing or unreadable Local State gives an empty map.
func profileNames(root string) map[string]string {
	out := map[string]string{}
	b, err := os.ReadFile(filepath.Join(root, "Local State"))
	if err != nil {
		return out
	}
	var ls struct {
		Profile struct {
			InfoCache map[string]struct {
				Name string `json:"name"`
			} `json:"info_cache"`
		} `json:"profile"`
	}
	if json.Unmarshal(b, &ls) == nil {
		for dir, info := range ls.Profile.InfoCache {
			out[dir] = info.Name
		}
	}
	return out
}

// Extensions lists the extensions a profile has installed from the Chrome
// Web Store or by policy, read from Preferences and Secure Preferences
// (Chrome keeps extensions.settings in either). Component and built-in
// extensions are left out.
func Extensions(prefsPaths ...string) ([]Extension, error) {
	seen := map[string]Extension{}
	var firstErr error
	read := 0
	for _, path := range prefsPaths {
		b, err := os.ReadFile(path)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		read++
		var prefs struct {
			Extensions struct {
				Settings map[string]json.RawMessage `json:"settings"`
			} `json:"extensions"`
		}
		if err := json.Unmarshal(b, &prefs); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", filepath.Base(path), err)
			}
			continue
		}
		for id, raw := range prefs.Extensions.Settings {
			var s struct {
				Location       int             `json:"location"`
				State          *int            `json:"state"`
				DisableReasons json.RawMessage `json:"disable_reasons"`
				Manifest       struct {
					Name string `json:"name"`
				} `json:"manifest"`
			}
			if json.Unmarshal(raw, &s) != nil {
				continue
			}
			// 1 internal (Web Store), 3 external pref, 4 registry, 6 external
			// policy download, 7 policy; 5 is component, 10 external component.
			switch s.Location {
			case 1, 3, 4, 6, 7, 8:
			default:
				continue
			}
			e := seen[id]
			e.ID = id
			if s.Manifest.Name != "" {
				e.Name = s.Manifest.Name
			}
			e.Enabled = !hasDisableReasons(s.DisableReasons) && (s.State == nil || *s.State == 1)
			seen[id] = e
		}
	}
	if read == 0 {
		return nil, firstErr
	}
	out := make([]Extension, 0, len(seen))
	for _, e := range seen {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// hasDisableReasons reports a non-empty disable_reasons, which Chrome writes
// as a bitmask int in older versions and a list in newer ones.
func hasDisableReasons(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s != "" && s != "0" && s != "[]" && s != "null"
}

func countCookies(path string) (int, error) {
	if _, err := os.Stat(path); err != nil {
		return 0, err
	}
	dir, err := os.MkdirTemp("", "agentcookie-backup-count-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(dir)
	cp := filepath.Join(dir, "db")
	if err := copyWithWAL(path, cp); err != nil {
		return 0, err
	}
	db, err := sql.Open("sqlite3", "file:"+cp+"?mode=ro")
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var n int
	if err := db.QueryRow("SELECT count(*) FROM cookies").Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// copySQLite copies a live SQLite database and its -wal to a private
// folder, folds the WAL in with VACUUM INTO, checks the result, and stores
// it gzipped at dst (dst minus ".gz" plus "-wal.gz" for a raw WAL).
//
// Chrome holds its databases in exclusive locking mode, so SQLite's online
// backup cannot read them. Instead the copy is retried until neither file
// changed while it was copied: a checkpoint that moves rows from the WAL
// into the database between the two copies would otherwise lose them.
//
// A copy that fails its integrity check on every try is stored as-is with
// its -wal (raw is true), so nothing is left out of the snapshot.
func copySQLite(src, dst string) (raw bool, err error) {
	if _, err := os.Stat(src); err != nil {
		return false, err
	}
	dir, err := os.MkdirTemp("", "agentcookie-backup-")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(dir)
	cp := filepath.Join(dir, "db")
	var lastErr error
	checkFailed := false
	for range copyTries {
		_ = os.Remove(cp + "-wal")
		stable, err := copyWithWALStable(src, cp)
		if err != nil {
			return false, err
		}
		if !stable {
			lastErr, checkFailed = errors.New("the database kept changing while it was copied"), false
			time.Sleep(copyRetryWait)
			continue
		}
		out := filepath.Join(dir, "vacuumed")
		_ = os.Remove(out)
		if err := vacuumInto(cp, out); err != nil {
			lastErr, checkFailed = err, true
			time.Sleep(copyRetryWait)
			continue
		}
		return false, gzipFile(out, dst)
	}
	if !checkFailed {
		return false, lastErr
	}
	// The last copy did not change while it was copied but fails its check
	// or cannot be opened: keep it raw.
	if err := gzipFile(cp, dst); err != nil {
		return false, err
	}
	if err := gzipFile(cp+"-wal", strings.TrimSuffix(dst, ".gz")+"-wal.gz"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	return true, nil
}

var (
	copyTries     = 5
	copyRetryWait = 500 * time.Millisecond
	errIntegrity  = errors.New("copy failed its integrity check")
)

// copyWithWALStable copies src and src-wal to dst and reports whether both
// files were unchanged (size and modification time) across the copy.
func copyWithWALStable(src, dst string) (bool, error) {
	before := fileStamps(src, src+"-wal")
	if err := copyWithWAL(src, dst); err != nil {
		return false, err
	}
	return before == fileStamps(src, src+"-wal"), nil
}

func fileStamps(paths ...string) string {
	var b strings.Builder
	for _, p := range paths {
		if info, err := os.Stat(p); err == nil {
			fmt.Fprintf(&b, "%d:%d;", info.Size(), info.ModTime().UnixNano())
		} else {
			b.WriteString("-;")
		}
	}
	return b.String()
}

// lockDir takes an exclusive lock on dir/.lock, waiting for another
// snapshot or prune to finish.
func lockDir(dir string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("chromebackup: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("chromebackup: lock: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

func vacuumInto(src, dst string) error {
	db, err := sql.Open("sqlite3", "file:"+src)
	if err != nil {
		return err
	}
	defer db.Close()
	var ok string
	if err := db.QueryRow("PRAGMA quick_check").Scan(&ok); err != nil {
		return fmt.Errorf("check copy: %w", err)
	}
	if ok != "ok" {
		return fmt.Errorf("%w: %s", errIntegrity, ok)
	}
	if _, err := db.Exec("VACUUM INTO ?", dst); err != nil {
		return fmt.Errorf("compact copy: %w", err)
	}
	return nil
}

func copyWithWAL(src, dst string) error {
	if err := copyFile(src, dst); err != nil {
		return err
	}
	if err := copyFile(src+"-wal", dst+"-wal"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func gzipFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	zw := gzip.NewWriter(out)
	if _, err := io.Copy(zw, in); err != nil {
		zw.Close()
		out.Close()
		return err
	}
	if err := zw.Close(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func gunzipFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	zr, err := gzip.NewReader(in)
	if err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(src), err)
	}
	defer zr.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	tmp := dst + ".agentcookie-restore"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, zr); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// copyTreeGzip copies a folder, gzipping each regular file. LevelDB's LOCK
// file is skipped. A symlink inside the folder is an error.
func copyTreeGzip(src, dst string) error {
	if _, err := os.Stat(src); err != nil {
		return err
	}
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil // Chrome removed it mid-walk
			}
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if d.Type()&fs.ModeSymlink != 0 {
			// Not followed, and not silently dropped: a restore replaces
			// the whole folder, so a skipped link would be lost.
			return fmt.Errorf("%s is a symlink; not copied", path)
		}
		if !d.Type().IsRegular() || d.Name() == "LOCK" {
			return nil
		}
		if err := gzipFile(path, target+".gz"); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	})
}

// restoreTreeGzip replaces dst with the gunzipped copy of a tree written by
// copyTreeGzip.
func restoreTreeGzip(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	// A fresh staging folder, so no existing sibling is ever removed.
	tmp, err := os.MkdirTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".agentcookie-restore-")
	if err != nil {
		return err
	}
	err = filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(tmp, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		return gunzipFile(path, strings.TrimSuffix(target, ".gz"))
	})
	if err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	if err := os.RemoveAll(dst); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

func writeManifest(dir string, m Manifest) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, manifestName+".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("chromebackup: %w", err)
	}
	return os.Rename(tmp, filepath.Join(dir, manifestName))
}

// List returns the snapshots in dir, newest first. Folders without a
// readable manifest (an interrupted snapshot) are skipped.
func List(dir string) ([]Snapshot, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Snapshot
	for _, e := range entries {
		if !e.IsDir() || strings.HasSuffix(e.Name(), ".partial") {
			continue
		}
		s, err := Load(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

// Load reads one snapshot folder.
func Load(path string) (Snapshot, error) {
	b, err := os.ReadFile(filepath.Join(path, manifestName))
	if err != nil {
		return Snapshot{}, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Manifest: m, Path: path}, nil
}

// MarkKeep pins a snapshot so Prune never removes it.
func MarkKeep(s Snapshot) error {
	s.Keep = true
	return writeManifest(s.Path, s.Manifest)
}
