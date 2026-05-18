// Package chromedirsync packs and unpacks Chrome on-disk profile
// directories as portable tarballs for cross-machine sync. Used for
// Chrome's Local Storage LevelDB and IndexedDB LevelDB stores, both of
// which are directory-of-files shapes that round-trip well through tar.
//
// Cookies use a different shape (single SQLite file with per-row
// encryption); they're handled in internal/chrome, not here.
package chromedirsync

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ErrSourceMissing is returned by Pack when the source directory does
// not exist on disk. Callers can treat this as "no payload to sync"
// rather than a hard error.
var ErrSourceMissing = errors.New("chromedirsync: source directory does not exist")

// Pack walks the directory rooted at dir, tars each file relative to dir,
// and returns the tarball bytes. Skips per-file errors that look like
// transient Chrome writes (file disappeared mid-walk); surfaces hard
// failures (permission denied, etc.) as errors.
//
// maxOriginSize caps the size of any single top-level subdirectory. Top-
// level subdirs are typically per-origin in Chrome's LevelDB layouts.
// Origins whose total size exceeds maxOriginSize are excluded from the
// payload, and their names are returned in the skipped list.
//
// Pass 0 for maxOriginSize to disable the per-origin size filter (sync
// everything).
func Pack(dir string, maxOriginSize int64) ([]byte, []string, error) {
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, ErrSourceMissing
		}
		return nil, nil, fmt.Errorf("chromedirsync: stat %s: %w", dir, err)
	}
	if !info.IsDir() {
		return nil, nil, fmt.Errorf("chromedirsync: %s is not a directory", dir)
	}

	skipped, err := selectOrigins(dir, maxOriginSize)
	if err != nil {
		return nil, nil, err
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	walkErr := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		if rel == "." {
			return nil
		}
		topLevel := firstPathSegment(rel)
		if _, dropped := skipped[topLevel]; dropped {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() {
			hdr := &tar.Header{
				Name:     rel + "/",
				Typeflag: tar.TypeDir,
				Mode:     int64(info.Mode().Perm()),
				ModTime:  info.ModTime(),
			}
			return tw.WriteHeader(hdr)
		}
		if info.Mode().IsRegular() {
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				if os.IsNotExist(readErr) {
					return nil
				}
				return readErr
			}
			hdr := &tar.Header{
				Name:     rel,
				Typeflag: tar.TypeReg,
				Mode:     int64(info.Mode().Perm()),
				Size:     int64(len(data)),
				ModTime:  info.ModTime(),
			}
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			if _, err := tw.Write(data); err != nil {
				return err
			}
		}
		return nil
	})
	if walkErr != nil {
		return nil, nil, fmt.Errorf("chromedirsync: walk %s: %w", dir, walkErr)
	}
	if err := tw.Close(); err != nil {
		return nil, nil, fmt.Errorf("chromedirsync: close tarball: %w", err)
	}
	skippedNames := make([]string, 0, len(skipped))
	for name := range skipped {
		skippedNames = append(skippedNames, name)
	}
	return buf.Bytes(), skippedNames, nil
}

// MaxUnpackBytes is the largest tar payload Unpack will accept. A
// payload over this size returns an error before any file write. The
// number matches the SinkSync httpserver body cap so a hostile sink
// cannot use a smaller payload-level cap to bypass the HTTP one.
const MaxUnpackBytes = 256 * 1024 * 1024

// MaxUnpackMembers caps the number of tar entries Unpack will process.
// A tarball with more entries returns an error. Defends against
// inode-exhaustion patterns where many tiny files exceed total-byte
// limits with much smaller total size.
const MaxUnpackMembers = 100_000

// Unpack untars payload into stagingDir. Caller is responsible for
// atomic-renaming stagingDir into place once Unpack returns. The
// staging-then-rename pattern keeps the live directory intact if Unpack
// fails partway through.
//
// v0.12 hardening: rejects payloads over MaxUnpackBytes, tarballs with
// more than MaxUnpackMembers entries, and any member whose path
// escapes stagingDir (absolute path, "..", or symlink). Symlink and
// hardlink members are unconditionally rejected.
func Unpack(payload []byte, stagingDir string) error {
	if int64(len(payload)) > MaxUnpackBytes {
		return fmt.Errorf("chromedirsync: payload %d bytes exceeds limit %d", len(payload), MaxUnpackBytes)
	}
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		return fmt.Errorf("chromedirsync: mkdir %s: %w", stagingDir, err)
	}
	absStaging, err := filepath.Abs(stagingDir)
	if err != nil {
		return fmt.Errorf("chromedirsync: abs %s: %w", stagingDir, err)
	}
	tr := tar.NewReader(bytes.NewReader(payload))
	members := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("chromedirsync: read tar header: %w", err)
		}
		members++
		if members > MaxUnpackMembers {
			return fmt.Errorf("chromedirsync: tarball exceeds %d members", MaxUnpackMembers)
		}
		dst, err := safeDestPath(absStaging, hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dst, os.FileMode(hdr.Mode)); err != nil {
				return fmt.Errorf("chromedirsync: mkdir %s: %w", dst, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return fmt.Errorf("chromedirsync: mkdir parent of %s: %w", dst, err)
			}
			f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return fmt.Errorf("chromedirsync: open %s: %w", dst, err)
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return fmt.Errorf("chromedirsync: write %s: %w", dst, err)
			}
			if err := f.Close(); err != nil {
				return fmt.Errorf("chromedirsync: close %s: %w", dst, err)
			}
		case tar.TypeSymlink, tar.TypeLink:
			return fmt.Errorf("chromedirsync: symlink / hardlink members not allowed (%q)", hdr.Name)
		default:
			// Unknown member types (devices, FIFOs, etc.) are also rejected.
			return fmt.Errorf("chromedirsync: unsupported tar entry type %d for %q", hdr.Typeflag, hdr.Name)
		}
	}
	return nil
}

// safeDestPath joins stagingAbs and member, then verifies the result
// stays under stagingAbs. Rejects absolute member paths and any path
// whose cleaned form escapes the staging directory (".." traversal).
func safeDestPath(stagingAbs, member string) (string, error) {
	if filepath.IsAbs(member) {
		return "", fmt.Errorf("chromedirsync: absolute tar path %q not allowed", member)
	}
	dst := filepath.Join(stagingAbs, member)
	cleaned := filepath.Clean(dst)
	if cleaned != stagingAbs && !strings.HasPrefix(cleaned, stagingAbs+string(filepath.Separator)) {
		return "", fmt.Errorf("chromedirsync: tar entry %q escapes staging dir", member)
	}
	return cleaned, nil
}

// AtomicReplaceDir renames stagingDir over liveDir as atomically as the
// filesystem allows. On macOS APFS this is a single rename(2) call.
// Existing liveDir is moved to liveDir.bak first; on success bak is
// removed. On failure the bak is restored.
func AtomicReplaceDir(stagingDir, liveDir string) error {
	if _, err := os.Stat(stagingDir); err != nil {
		return fmt.Errorf("chromedirsync: staging dir %s missing: %w", stagingDir, err)
	}
	bakDir := liveDir + ".agentcookie.bak"
	_ = os.RemoveAll(bakDir)
	if _, err := os.Stat(liveDir); err == nil {
		if err := os.Rename(liveDir, bakDir); err != nil {
			return fmt.Errorf("chromedirsync: backup %s: %w", liveDir, err)
		}
	}
	if err := os.Rename(stagingDir, liveDir); err != nil {
		// Roll back.
		if _, statErr := os.Stat(bakDir); statErr == nil {
			_ = os.Rename(bakDir, liveDir)
		}
		return fmt.Errorf("chromedirsync: rename staging into place: %w", err)
	}
	_ = os.RemoveAll(bakDir)
	return nil
}

// selectOrigins returns the set of top-level subdir names whose total
// size exceeds maxOriginSize. These are returned for exclusion from the
// payload. When maxOriginSize is 0, returns an empty set.
func selectOrigins(dir string, maxOriginSize int64) (map[string]struct{}, error) {
	skipped := map[string]struct{}{}
	if maxOriginSize <= 0 {
		return skipped, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("chromedirsync: read %s: %w", dir, err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		size, err := dirSize(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		if size > maxOriginSize {
			skipped[entry.Name()] = struct{}{}
		}
	}
	return skipped, nil
}

func dirSize(path string) (int64, error) {
	var total int64
	err := filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total, err
}

func firstPathSegment(p string) string {
	for i := 0; i < len(p); i++ {
		if p[i] == filepath.Separator {
			return p[:i]
		}
	}
	return p
}
