//go:build !windows

package daemon

import "syscall"

// detachSysProcAttr makes the child a new session/group leader so it survives the
// parent exit and so killTree can target the whole group.
func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
