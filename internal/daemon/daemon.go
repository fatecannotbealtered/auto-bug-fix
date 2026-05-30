// Package daemon manages the background poller process lifecycle:
// detached start, stop (whole process tree), and status, via a PID file.
package daemon

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// ReadPID reads the recorded poller PID. Returns an os.IsNotExist error if absent,
// or an error if the file content is not a positive integer.
func ReadPID(pidPath string) (int, error) {
	data, err := os.ReadFile(pidPath)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("invalid pid file %s: %w", pidPath, err)
	}
	if pid <= 0 {
		return 0, fmt.Errorf("invalid pid file %s: non-positive pid %d", pidPath, pid)
	}
	return pid, nil
}

func writePID(pidPath string, pid int) error {
	return os.WriteFile(pidPath, []byte(strconv.Itoa(pid)), 0o600)
}

// Status reports whether the recorded poller is alive.
func Status(pidPath string) (running bool, pid int, err error) {
	pid, err = ReadPID(pidPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, 0, nil
	}
	if err != nil {
		return false, 0, err
	}
	return processAlive(pid), pid, nil
}

// StartDetached launches binPath with args as a background, terminal-detached
// process, recording its PID. If a recorded poller is already alive, it returns
// (pid, true, nil) without spawning a second one.
// The already-running check is best-effort: it assumes a single caller invokes
// StartDetached at a time (a human or one scheduled launch), not concurrent OS
// processes. There is no cross-process lock on the PID file.
func StartDetached(binPath, pidPath, logPath string, args []string) (int, bool, error) {
	if pid, err := ReadPID(pidPath); err == nil && processAlive(pid) {
		return pid, true, nil
	}

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, false, fmt.Errorf("open log %s: %w", logPath, err)
	}
	defer logFile.Close()

	cmd := exec.Command(binPath, args...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = detachSysProcAttr()
	if err := cmd.Start(); err != nil {
		return 0, false, fmt.Errorf("start detached: %w", err)
	}

	pid := cmd.Process.Pid
	if err := writePID(pidPath, pid); err != nil {
		return pid, false, fmt.Errorf("write pid file: %w", err)
	}
	_ = cmd.Process.Release() // detach: keep it running after we exit; a Release error is non-fatal since the child is already started
	return pid, false, nil
}

// Stop terminates the recorded poller and its child tree, then removes the PID
// file. A missing PID file or already-dead process is treated as success.
func Stop(pidPath string) error {
	pid, err := ReadPID(pidPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if processAlive(pid) {
		if err := killTree(pid); err != nil {
			return fmt.Errorf("kill pid %d: %w", pid, err)
		}
	}
	if err := os.Remove(pidPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove pid file %s: %w", pidPath, err)
	}
	return nil
}
