//go:build !windows

package daemon

import (
	"fmt"
	"syscall"
	"time"
)

// processAlive reports whether pid is a live process.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// killTree terminates the poller and its spawned children. The detached poller
// is a session/group leader (Setsid), so a negative pid targets the whole group.
func killTree(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("killTree: invalid pid %d", pid)
	}
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
		return err
	}
	for i := 0; i < 50; i++ {
		// Reap pid if it is our own child; otherwise Wait4 returns ECHILD and
		// is a harmless no-op. Without this, a terminated child of the current
		// process lingers as a zombie that still answers kill(pid, 0).
		_, _ = syscall.Wait4(pid, nil, syscall.WNOHANG, nil)
		if !processAlive(pid) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		return err
	}
	_, _ = syscall.Wait4(pid, nil, syscall.WNOHANG, nil)
	return nil
}
