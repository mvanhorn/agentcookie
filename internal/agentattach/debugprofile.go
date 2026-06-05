package agentattach

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/mvanhorn/agentcookie/internal/cdp"
	"github.com/mvanhorn/agentcookie/internal/chrome"
)

// DefaultDebugProfileDir is the agentcookie-owned Chrome user-data-dir
// used for the fallback attach path. It is deliberately NOT the user's
// default profile: Chrome 136+ refuses --remote-debugging-port on the
// default dir, and a non-default dir gets its own encryption key, so the
// debug port never exposes the real profile's jar.
func DefaultDebugProfileDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".agentcookie", "chrome-debug")
}

// DebugProfile is the fallback target: a dedicated Chrome profile that
// agentcookie seeds from the user's default profile (cookies + localStorage)
// and launches with a loopback remote-debugging port, so an agent browser
// can attach to it when the real default profile cannot be attached
// (older Chrome, or the user declining the chrome://inspect path).
//
// Device-bound (DBSC) sessions do not transfer to this profile -- the
// binding key lives in the OS keystore tied to the originating browser --
// so a few sites may still read as logged-out here. The caller surfaces
// the DBSC-skipped count.
type DebugProfile struct {
	Dir          string
	Port         int
	ChromeBinary string
}

// NewDebugProfile returns a DebugProfile for the given loopback port.
func NewDebugProfile(port int) *DebugProfile {
	return &DebugProfile{
		Dir:          DefaultDebugProfileDir(),
		Port:         port,
		ChromeBinary: macChromeBinary,
	}
}

// EnsureDir creates the debug profile dir if absent.
func (dp *DebugProfile) EnsureDir() error {
	return os.MkdirAll(dp.Dir, 0o700)
}

// SeedCookies injects the given (already filtered + DBSC-classified)
// cookies into the debug profile's SQLite via a one-shot headless Chrome.
// It must run while no persistent Chrome holds the profile dir. An empty
// cookie set is a no-op.
func (dp *DebugProfile) SeedCookies(ctx context.Context, cookies []chrome.Cookie) error {
	if err := dp.EnsureDir(); err != nil {
		return err
	}
	return cdp.InjectCookies(ctx, dp.Dir, cookies)
}

// localStorageRel is the Chrome profile-relative path to the localStorage
// LevelDB tree.
var localStorageRel = filepath.Join("Local Storage")

// CopyLocalStorage copies the localStorage LevelDB tree from srcProfileDir
// into the debug profile (KTD6: file copy, no LevelDB parse). It must run
// while neither Chrome holds either profile's LevelDB lock. Returns the
// number of files copied. A missing source localStorage tree is not an
// error (returns 0).
func (dp *DebugProfile) CopyLocalStorage(srcProfileDir string) (int, error) {
	src := filepath.Join(srcProfileDir, localStorageRel)
	if _, err := os.Stat(src); err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	if err := dp.EnsureDir(); err != nil {
		return 0, err
	}
	dst := filepath.Join(dp.Dir, localStorageRel)
	return copyTree(src, dst)
}

// LaunchArgs builds the Chrome flags for the persistent debug instance.
// The port is bound to loopback only (KTD7) and points at the dedicated
// dir, never the user's default profile.
func (dp *DebugProfile) LaunchArgs() []string {
	return []string{
		fmt.Sprintf("--remote-debugging-port=%d", dp.Port),
		"--remote-debugging-address=127.0.0.1",
		fmt.Sprintf("--user-data-dir=%s", dp.Dir),
		"--no-first-run",
		"--no-default-browser-check",
	}
}

// Launch starts the persistent debug Chrome and waits until its CDP
// endpoint answers. The returned *exec.Cmd is owned by the caller (call
// Stop to terminate). The dir must already be seeded.
func (dp *DebugProfile) Launch(ctx context.Context) (*exec.Cmd, error) {
	if err := dp.EnsureDir(); err != nil {
		return nil, err
	}
	cmd := exec.Command(dp.ChromeBinary, dp.LaunchArgs()...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("agentattach: launch debug Chrome: %w", err)
	}
	if err := dp.waitReachable(ctx); err != nil {
		_ = stopProcess(cmd)
		return nil, err
	}
	return cmd, nil
}

// Stop terminates a debug Chrome started by Launch.
func (dp *DebugProfile) Stop(cmd *exec.Cmd) error {
	return stopProcess(cmd)
}

// waitReachable polls the debug endpoint until it answers or ctx/deadline
// elapses.
func (dp *DebugProfile) waitReachable(ctx context.Context) error {
	deadline := time.Now().Add(20 * time.Second)
	for {
		if Discover(ctx, dp.Port).Reachable {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("agentattach: debug Chrome did not expose CDP on 127.0.0.1:%d within timeout", dp.Port)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func stopProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if err := cmd.Process.Kill(); err != nil {
		return err
	}
	_, _ = cmd.Process.Wait()
	return nil
}

// copyTree recursively copies the directory tree at src into dst,
// returning the number of regular files written.
func copyTree(src, dst string) (int, error) {
	count := 0
	err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if !info.Mode().IsRegular() {
			return nil // skip sockets/symlinks/etc.
		}
		if err := copyFile(path, target); err != nil {
			return err
		}
		count++
		return nil
	})
	return count, err
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
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
