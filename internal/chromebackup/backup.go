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
	"sort"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3" // registers the sqlite3 driver
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

// sqliteFiles are SQLite databases: copied with their -wal and compacted so
// the snapshot holds every committed row.
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
	Dir        string      `json:"dir"`
	Name       string      `json:"name,omitempty"`
	Cookies    int         `json:"cookies"`
	Extensions []Extension `json:"extensions"`
	Files      []string    `json:"files"`
	Errors     []string    `json:"errors,omitempty"`
}

// Manifest describes one snapshot.
type Manifest struct {
	ID       string    `json:"id"`
	Taken    time.Time `json:"taken"`
	Heavy    bool      `json:"heavy"`
	Reason   string    `json:"reason,omitempty"`
	Keep     bool      `json:"keep,omitempty"`
	Profiles []Profile `json:"profiles"`
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
// HeavyFiles and HeavyDirs. A file that fails to copy is recorded in the
// profile's Errors and does not stop the others.
func Take(root, dir string, now time.Time, heavy bool, reason string) (Snapshot, error) {
	profiles, err := profileDirs(root)
	if err != nil {
		return Snapshot{}, err
	}
	if len(profiles) == 0 {
		return Snapshot{}, fmt.Errorf("chromebackup: no Chrome profiles under %s", root)
	}
	names := profileNames(root)
	id := now.UTC().Format(idLayout)
	final := filepath.Join(dir, id)
	if _, err := os.Stat(final); err == nil {
		return Snapshot{}, fmt.Errorf("chromebackup: snapshot %s already exists", id)
	}
	tmp := final + ".partial"
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return Snapshot{}, fmt.Errorf("chromebackup: %w", err)
	}
	m := Manifest{ID: id, Taken: now.UTC(), Heavy: heavy, Reason: reason}
	if err := gzipFile(filepath.Join(root, "Local State"), filepath.Join(tmp, "Local State.gz")); err != nil && !errors.Is(err, fs.ErrNotExist) {
		_ = os.RemoveAll(tmp)
		return Snapshot{}, fmt.Errorf("chromebackup: Local State: %w", err)
	}
	for _, p := range profiles {
		m.Profiles = append(m.Profiles, snapshotProfile(root, tmp, p, names[p], heavy))
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

func snapshotProfile(root, snap, dir, name string, heavy bool) Profile {
	p := Profile{Dir: dir, Name: name, Cookies: -1}
	src := filepath.Join(root, dir)
	dst := filepath.Join(snap, dir)
	if err := os.MkdirAll(dst, 0o700); err != nil {
		p.Errors = append(p.Errors, err.Error())
		return p
	}
	files := append([]string{}, LightFiles...)
	if heavy {
		files = append(files, HeavyFiles...)
	}
	for _, f := range files {
		var err error
		if sqliteFiles[f] {
			err = copySQLite(filepath.Join(src, f), filepath.Join(dst, f+".gz"))
		} else {
			err = gzipFile(filepath.Join(src, f), filepath.Join(dst, f+".gz"))
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
		for _, d := range HeavyDirs {
			err := copyTreeGzip(filepath.Join(src, d), filepath.Join(dst, d))
			switch {
			case errors.Is(err, fs.ErrNotExist):
			case err != nil:
				p.Errors = append(p.Errors, fmt.Sprintf("%s: %v", d, err))
			default:
				p.Files = append(p.Files, d+"/")
			}
		}
	}
	if n, err := countCookies(filepath.Join(src, "Cookies")); err == nil {
		p.Cookies = n
	} else if !errors.Is(err, fs.ErrNotExist) {
		p.Errors = append(p.Errors, fmt.Sprintf("count cookies: %v", err))
	}
	exts, err := Extensions(filepath.Join(src, "Preferences"), filepath.Join(src, "Secure Preferences"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		p.Errors = append(p.Errors, fmt.Sprintf("extensions: %v", err))
	}
	p.Extensions = exts
	if p.Extensions == nil {
		p.Extensions = []Extension{}
	}
	return p
}

// profileDirs lists the profile folders under root that have Preferences.
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
		if _, err := os.Stat(filepath.Join(root, e.Name(), "Preferences")); err == nil {
			out = append(out, e.Name())
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
// it gzipped at dst.
func copySQLite(src, dst string) error {
	if _, err := os.Stat(src); err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "agentcookie-backup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	cp := filepath.Join(dir, "db")
	if err := copyWithWAL(src, cp); err != nil {
		return err
	}
	out := filepath.Join(dir, "vacuumed")
	if err := vacuumInto(cp, out); err != nil {
		return err
	}
	return gzipFile(out, dst)
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
		return fmt.Errorf("copy failed its integrity check: %s", ok)
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
// file is skipped.
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
	tmp := dst + ".agentcookie-restore"
	_ = os.RemoveAll(tmp)
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
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
