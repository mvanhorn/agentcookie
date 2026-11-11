package e2e

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// stopOwnedFixtureGroup may only receive a directly launched, Setsid child
// which has never been Waited or Released. Keeping that parent unreaped pins
// its PID/group ID even after an early exit. The complete group is signaled
// before Wait releases that identity; no PID from a persisted file is used.
func stopOwnedFixtureGroup(cmd *exec.Cmd, grace time.Duration) error {
	if cmd == nil || cmd.Process == nil || cmd.ProcessState != nil || cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setsid {
		return fmt.Errorf("cleanup requires an unreaped owned session leader")
	}
	pid := cmd.Process.Pid
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("terminate owned group: %w", err)
	}
	time.Sleep(grace)
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("kill owned group: %w", err)
	}
	err := cmd.Wait()
	var exited *exec.ExitError
	if err != nil && !errors.As(err, &exited) {
		return fmt.Errorf("reap owned parent: %w", err)
	}
	return nil
}

// The fixture's callers/daemons must already be stopped before final cleanup,
// so no new broker launch can race this lifetime-lock check. Stale state or
// socket files do not authorize signals to the process ID written in them.
func waitFixtureBrokerStopped(ctx context.Context, lockPath string) error {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		file, err := os.OpenFile(lockPath, os.O_RDWR, 0)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			unlockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
			closeErr := file.Close()
			return errors.Join(unlockErr, closeErr)
		}
		_ = file.Close()
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("broker lifetime lock still held: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}
