//go:build windows

package daemon

import "syscall"

// detachedProcess (DETACHED_PROCESS) detaches the child from the parent console.
const detachedProcess = 0x00000008

// detachSysProcAttr launches the child detached, in its own process group so
// taskkill /T can terminate the whole tree.
func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: detachedProcess | syscall.CREATE_NEW_PROCESS_GROUP,
	}
}
