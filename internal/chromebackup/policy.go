package chromebackup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// Every is how often a snapshot is due.
	Every = time.Hour
	// heavyEvery spaces the snapshots that also copy History and extension
	// storage.
	heavyEvery = 23 * time.Hour
	// keepAll keeps every snapshot this young.
	keepAll = 24 * time.Hour
	// keepDaily keeps the newest snapshot of each day this young.
	keepDaily = 7 * 24 * time.Hour
	// keepPinned keeps a snapshot pinned by a wipe warning this long.
	keepPinned = 30 * 24 * time.Hour

	// A wipe warning needs at least this many cookies or extensions before.
	minCookiesForWarning    = 200
	minExtensionsForWarning = 3
)

// Plan says whether a snapshot is due at now, and whether it should be
// heavy, given the existing snapshots (newest first).
func Plan(snaps []Snapshot, now time.Time) (due, heavy bool) {
	heavy = true
	for _, s := range snaps {
		if s.Heavy && now.Sub(s.Taken) < heavyEvery {
			heavy = false
			break
		}
	}
	if len(snaps) == 0 {
		return true, heavy
	}
	return now.Sub(snaps[0].Taken) >= Every, heavy
}

// Warnings compares a new snapshot with the one before it and describes
// any profile that lost most of its cookies or extensions, or vanished.
func Warnings(prev, cur Manifest) []string {
	now := map[string]Profile{}
	for _, p := range cur.Profiles {
		now[p.Dir] = p
	}
	var out []string
	for _, before := range prev.Profiles {
		label := before.Dir
		if before.Name != "" {
			label = fmt.Sprintf("%s (%s)", before.Name, before.Dir)
		}
		after, ok := now[before.Dir]
		if !ok {
			if before.Cookies >= minCookiesForWarning || len(before.Extensions) >= minExtensionsForWarning {
				out = append(out, fmt.Sprintf("Chrome profile %s is gone", label))
			}
			continue
		}
		if before.Cookies >= minCookiesForWarning && after.Cookies >= 0 && after.Cookies*2 < before.Cookies {
			out = append(out, fmt.Sprintf("Chrome profile %s dropped from %d cookies to %d", label, before.Cookies, after.Cookies))
		}
		if len(before.Extensions) >= minExtensionsForWarning && len(after.Extensions)*2 < len(before.Extensions) {
			out = append(out, fmt.Sprintf("Chrome profile %s dropped from %d extensions to %d", label, len(before.Extensions), len(after.Extensions)))
		}
	}
	return out
}

// Prune removes snapshots outside the retention: all from the last 24
// hours, the newest and newest heavy of each day for 7 days, and pinned ones for 30 days.
// Unfinished snapshot folders older than an hour go too. It returns the
// removed IDs.
func Prune(dir string, now time.Time) ([]string, error) {
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	unlock, err := lockDir(dir)
	if err != nil {
		return nil, err
	}
	defer unlock()
	snaps, err := List(dir)
	if err != nil {
		return nil, err
	}
	var removed []string
	// Past a day old, keep the newest snapshot of each day and the newest
	// heavy one, so History and extension storage stay recoverable too.
	seenDay, seenHeavyDay := map[string]bool{}, map[string]bool{}
	for _, s := range snaps { // newest first
		age := now.Sub(s.Taken)
		day := s.Taken.Local().Format("2006-01-02")
		daily := age < keepDaily && (!seenDay[day] || (s.Heavy && !seenHeavyDay[day]))
		keep := age < keepAll || (s.Keep && age < keepPinned) || daily
		if age >= keepAll && age < keepDaily {
			seenDay[day] = true
			if s.Heavy {
				seenHeavyDay[day] = true
			}
		}
		if keep {
			continue
		}
		if err := os.RemoveAll(s.Path); err != nil {
			return removed, err
		}
		removed = append(removed, s.ID)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".partial") {
			continue
		}
		if info, err := e.Info(); err == nil && now.Sub(info.ModTime()) > time.Hour {
			_ = os.RemoveAll(filepath.Join(dir, e.Name()))
		}
	}
	return removed, nil
}

// Result is what one scheduled run did.
type Result struct {
	Taken    *Snapshot
	Warnings []string
	// Good is the snapshot taken before the one that showed a wipe, pinned
	// so it is kept; nil without warnings.
	Good    *Snapshot
	Removed []string
}

// RunDue takes a snapshot if one is due, compares it with the previous
// one, pins the previous one when the comparison shows a wipe, and prunes.
// extraDirs go into a heavy snapshot (see Take).
func RunDue(root, dir string, now time.Time, extraDirs []string) (Result, error) {
	snaps, err := List(dir)
	if err != nil {
		return Result{}, err
	}
	due, heavy := Plan(snaps, now)
	if !due {
		return Result{}, nil
	}
	s, err := Take(root, dir, now, heavy, "scheduled", extraDirs)
	if err != nil {
		return Result{}, err
	}
	res := Result{Taken: &s}
	if len(snaps) > 0 {
		prev := snaps[0]
		res.Warnings = Warnings(prev.Manifest, s.Manifest)
		if len(res.Warnings) > 0 {
			if err := MarkKeep(prev); err == nil {
				prev.Keep = true
			}
			res.Good = &prev
		}
	}
	removed, err := Prune(dir, now)
	res.Removed = removed
	return res, err
}

// LastGood picks the snapshot to restore by default: the one before the
// newest snapshot that showed a wipe, else the newest.
func LastGood(snaps []Snapshot) (Snapshot, error) {
	if len(snaps) == 0 {
		return Snapshot{}, errors.New("chromebackup: no snapshots yet")
	}
	for i := 0; i+1 < len(snaps); i++ {
		if len(Warnings(snaps[i+1].Manifest, snaps[i].Manifest)) > 0 {
			return snaps[i+1], nil
		}
	}
	return snaps[0], nil
}

// Restore copies a snapshot's files back into root, for the given profile
// folders (all in the snapshot when empty). Local State is restored only
// when localState is set. Chrome must not be running; the caller checks.
func Restore(s Snapshot, root string, profiles []string, localState bool) ([]string, error) {
	want := map[string]bool{}
	for _, p := range profiles {
		want[p] = true
	}
	var done []string
	matched := 0
	for _, p := range s.Profiles {
		if len(want) > 0 && !want[p.Dir] {
			continue
		}
		matched++
		dst := filepath.Join(root, p.Dir)
		if err := os.MkdirAll(dst, 0o700); err != nil {
			return done, err
		}
		for _, f := range p.Files {
			src := filepath.Join(s.Path, p.Dir)
			if d, ok := strings.CutSuffix(f, "/"); ok {
				if err := restoreTreeGzip(filepath.Join(src, d), filepath.Join(dst, d)); err != nil {
					return done, fmt.Errorf("%s/%s: %w", p.Dir, d, err)
				}
			} else {
				if err := gunzipFile(filepath.Join(src, f+".gz"), filepath.Join(dst, f)); err != nil {
					return done, fmt.Errorf("%s/%s: %w", p.Dir, f, err)
				}
				if sqliteFiles[filepath.Base(f)] {
					for _, side := range []string{"-wal", "-journal", "-shm"} {
						if err := os.Remove(filepath.Join(dst, f+side)); err != nil && !errors.Is(err, fs.ErrNotExist) {
							return done, err
						}
					}
				}
			}
			done = append(done, filepath.Join(p.Dir, f))
		}
	}
	if len(want) > 0 && matched < len(want) {
		return done, fmt.Errorf("chromebackup: snapshot %s does not have every profile asked for", s.ID)
	}
	if localState {
		err := gunzipFile(filepath.Join(s.Path, "Local State.gz"), filepath.Join(root, "Local State"))
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return done, fmt.Errorf("restore Local State: %w", err)
		default:
			done = append(done, "Local State")
		}
	}
	return done, nil
}

// Size returns the bytes a snapshot uses on disk.
func Size(s Snapshot) int64 {
	var n int64
	_ = filepath.WalkDir(s.Path, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}
