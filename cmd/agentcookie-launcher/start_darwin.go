//go:build darwin

package main

/*
#include <spawn.h>
#include <stdlib.h>

// Private libSystem call that Terminal and other launchers use so a child is
// responsible for its own privacy (TCC) checks.
int responsibility_spawnattrs_setdisclaim(posix_spawnattr_t *attrs, int disclaim);

static int spawn_disclaimed(pid_t *pid, const char *path, char *const argv[], char *const envp[]) {
	posix_spawnattr_t attrs;
	int rc = posix_spawnattr_init(&attrs);
	if (rc != 0) {
		return rc;
	}
	rc = responsibility_spawnattrs_setdisclaim(&attrs, 1);
	if (rc == 0) {
		rc = posix_spawn(pid, path, NULL, &attrs, argv, envp);
	}
	posix_spawnattr_destroy(&attrs);
	return rc;
}
*/
import "C"

import (
	"os"
	"os/signal"
	"syscall"
	"unsafe"
)

var forwarded = []os.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGUSR1, syscall.SIGUSR2}

// start spawns path as a child that disclaims responsibility, forwards
// signals to it, and returns its exit code. A child killed by a signal
// yields 128+signal, so launchd still sees a non-zero exit and relaunches.
func start(path string, argv, env []string) (int, error) {
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, forwarded...)
	defer signal.Stop(sigs)

	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))
	cargv := cStrings(argv)
	defer freeCStrings(cargv)
	cenv := cStrings(env)
	defer freeCStrings(cenv)

	var pid C.pid_t
	if rc := C.spawn_disclaimed(&pid, cpath, &cargv[0], &cenv[0]); rc != 0 {
		return 0, syscall.Errno(rc)
	}
	child := int(pid)
	go func() {
		for s := range sigs {
			_ = syscall.Kill(child, s.(syscall.Signal))
		}
	}()
	for {
		var ws syscall.WaitStatus
		_, err := syscall.Wait4(child, &ws, 0, nil)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return 1, nil
		}
		switch {
		case ws.Exited():
			return ws.ExitStatus(), nil
		case ws.Signaled():
			return 128 + int(ws.Signal()), nil
		}
	}
}

func cStrings(ss []string) []*C.char {
	out := make([]*C.char, len(ss)+1)
	for i, s := range ss {
		out[i] = C.CString(s)
	}
	return out
}

func freeCStrings(cs []*C.char) {
	for _, c := range cs {
		if c != nil {
			C.free(unsafe.Pointer(c))
		}
	}
}
