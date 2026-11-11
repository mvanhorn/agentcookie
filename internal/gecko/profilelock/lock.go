// Package profilelock implements Mozilla's closed-profile exclusion on Unix.
package profilelock

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

var ErrBrowserRunning = errors.New("gecko browser is running; closed-profile read refused")
var held sync.Map

// Lock is held until Close. fcntl locks are per-process, so held additionally
// prevents two readers in this process from releasing each other's exclusion.
type Lock struct {
	f    *os.File
	path string
	once sync.Once
}

func Acquire(profile string) (*Lock, error) {
	p, e := filepath.EvalSymlinks(profile)
	if e != nil {
		return nil, e
	}
	if _, e = os.Stat(filepath.Join(p, "WebDriverBiDiServer.json")); e == nil {
		return nil, ErrBrowserRunning
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	if runtime.GOOS == "linux" {
		if target, e := os.Readlink(filepath.Join(p, "lock")); e == nil {
			if linuxLockLive(target) {
				return nil, ErrBrowserRunning
			}
		} else if !os.IsNotExist(e) {
			return nil, fmt.Errorf("cannot establish profile lock: %w", e)
		}
	}
	if _, loaded := held.LoadOrStore(p, true); loaded {
		return nil, ErrBrowserRunning
	}
	f, e := os.OpenFile(filepath.Join(p, ".parentlock"), os.O_RDWR|os.O_CREATE, 0600)
	if e != nil {
		held.Delete(p)
		return nil, e
	}
	fl := syscall.Flock_t{Type: syscall.F_WRLCK, Whence: 0, Start: 0, Len: 0}
	if e = syscall.FcntlFlock(f.Fd(), syscall.F_SETLK, &fl); e != nil {
		f.Close()
		held.Delete(p)
		return nil, fmt.Errorf("%w: %v", ErrBrowserRunning, e)
	}
	return &Lock{f: f, path: p}, nil
}
func linuxLockLive(target string) bool {
	parts := strings.SplitN(target, ":", 2)
	if len(parts) != 2 {
		return true
	}
	host := parts[0]
	local := host == "127.0.0.1" || host == "localhost"
	hostname, _ := os.Hostname()
	local = local || host == hostname
	if addrs, e := net.InterfaceAddrs(); e == nil {
		for _, a := range addrs {
			ip, _, e := net.ParseCIDR(a.String())
			if e == nil && ip.String() == host {
				local = true
			}
		}
	}
	if !local {
		return true
	}
	pid, e := strconv.Atoi(strings.TrimPrefix(parts[1], "+"))
	if e != nil || pid <= 0 {
		return true
	}
	e = syscall.Kill(pid, 0)
	return e == nil || e != syscall.ESRCH
}
func (l *Lock) Close() error {
	var e error
	l.once.Do(func() { e = l.f.Close(); held.Delete(l.path) })
	return e
}
