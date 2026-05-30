package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if os.Getenv("DAEMON_HELPER_SLEEP") == "1" {
		time.Sleep(60 * time.Second)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestReadPID_RejectsNonPositive(t *testing.T) {
	pidPath := filepath.Join(t.TempDir(), "poller.pid")
	if err := os.WriteFile(pidPath, []byte("0"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPID(pidPath); err == nil {
		t.Error("ReadPID should reject a non-positive pid")
	}
}

func TestStatus_NoFile(t *testing.T) {
	pidPath := filepath.Join(t.TempDir(), "poller.pid")
	running, pid, err := Status(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	if running || pid != 0 {
		t.Errorf("no pid file should be not-running, got running=%v pid=%d", running, pid)
	}
}

func TestStatus_CurrentProcessAlive(t *testing.T) {
	pidPath := filepath.Join(t.TempDir(), "poller.pid")
	if err := writePID(pidPath, os.Getpid()); err != nil {
		t.Fatal(err)
	}
	running, pid, err := Status(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	if !running || pid != os.Getpid() {
		t.Errorf("current process should be alive, got running=%v pid=%d", running, pid)
	}
}

func TestStartDetached_AlreadyRunning(t *testing.T) {
	pidPath := filepath.Join(t.TempDir(), "poller.pid")
	logPath := filepath.Join(t.TempDir(), "poller.log")
	if err := writePID(pidPath, os.Getpid()); err != nil { // a known-alive PID
		t.Fatal(err)
	}
	pid, already, err := StartDetached("does-not-matter", pidPath, logPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !already || pid != os.Getpid() {
		t.Errorf("expected already-running with current pid, got already=%v pid=%d", already, pid)
	}
}

func TestStop_NoFile(t *testing.T) {
	pidPath := filepath.Join(t.TempDir(), "poller.pid")
	if err := Stop(pidPath); err != nil {
		t.Errorf("Stop with no pid file should be nil, got %v", err)
	}
}

func TestStartDetached_And_Stop(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "poller.pid")
	logPath := filepath.Join(dir, "poller.log")

	t.Setenv("DAEMON_HELPER_SLEEP", "1")
	pid, already, err := StartDetached(os.Args[0], pidPath, logPath, []string{"-test.run=TestMain"})
	if err != nil {
		t.Fatal(err)
	}
	if already {
		t.Fatal("should not report already-running on first start")
	}
	if !processAlive(pid) {
		t.Fatalf("child %d should be alive after start", pid)
	}
	if err := Stop(pidPath); err != nil {
		t.Fatal(err)
	}
	dead := false
	for i := 0; i < 20; i++ {
		if !processAlive(pid) {
			dead = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !dead {
		t.Errorf("child %d should be dead after Stop", pid)
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Errorf("pid file should be removed after Stop")
	}
}
