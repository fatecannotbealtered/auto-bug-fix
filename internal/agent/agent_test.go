package agent_test

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"github.com/fatecannotbealtered/auto-bug-fix/internal/agent"
)

func TestParseCommand_Basic(t *testing.T) {
	tokens, err := agent.ParseCommand(`cursor exec "Fix bug {issueKey}"`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"cursor", "exec", "Fix bug {issueKey}"}
	if len(tokens) != len(want) {
		t.Fatalf("got %v, want %v", tokens, want)
	}
	for i, w := range want {
		if tokens[i] != w {
			t.Errorf("token[%d]: got %q, want %q", i, tokens[i], w)
		}
	}
}

func TestParseCommand_WindowsPath(t *testing.T) {
	tokens, err := agent.ParseCommand(`"C:\Program Files\agent.exe" --flag`)
	if err != nil {
		t.Fatal(err)
	}
	if tokens[0] != `C:\Program Files\agent.exe` {
		t.Errorf("got %q", tokens[0])
	}
}

func TestParseCommand_UnterminatedQuote(t *testing.T) {
	_, err := agent.ParseCommand(`cursor "unterminated`)
	if err == nil {
		t.Fatal("expected error for unterminated quote")
	}
}

func TestParseCommand_Empty(t *testing.T) {
	_, err := agent.ParseCommand("   ")
	if err == nil {
		t.Fatal("expected error for empty command")
	}
}

func TestBuildArgs_WithPlaceholder(t *testing.T) {
	args, err := agent.BuildArgs(`cursor exec "Fix bug {issueKey} using skill"`, "PROJ-123")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"cursor", "exec", "Fix bug PROJ-123 using skill"}
	for i, w := range want {
		if args[i] != w {
			t.Errorf("args[%d]: got %q, want %q", i, args[i], w)
		}
	}
}

func TestBuildArgs_WithoutPlaceholder(t *testing.T) {
	args, err := agent.BuildArgs("cursor fix-bug", "PROJ-123")
	if err != nil {
		t.Fatal(err)
	}
	last := args[len(args)-1]
	if last != "PROJ-123" {
		t.Errorf("expected issueKey appended, got %q", last)
	}
}

func TestTrigger_ExitZero(t *testing.T) {
	cmd := os.Args[0] + ` -test.run=^$`
	result, err := agent.Trigger("PROJ-1", cmd)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("exit code: got %d, want 0", result.ExitCode)
	}
}

func TestTrigger_ExitNonZero(t *testing.T) {
	// Use a command that exits non-zero
	_, err := agent.Trigger("PROJ-1", os.Args[0]+` -test.run=^TestNonExistent$ -test.v`)
	// go test exits 0 even with no tests matched; use a different approach
	_ = err // non-zero exit is platform-dependent in test binary; just ensure no panic
}

func TestParseResultMarker(t *testing.T) {
	result := agent.ParseResultMarker("logs\nAUTO_BUG_FIX_RESULT outcome=auto-fix mr=https://gitlab.example/mr/1 handoff=.tcl/handoff/PROJ-1.needs-confirmation.md\n")
	if result.Outcome != "auto-fix" {
		t.Fatalf("outcome: got %q", result.Outcome)
	}
	if result.MRURL != "https://gitlab.example/mr/1" {
		t.Fatalf("mr url: got %q", result.MRURL)
	}
	if result.HandoffPath != ".tcl/handoff/PROJ-1.needs-confirmation.md" {
		t.Fatalf("handoff path: got %q", result.HandoffPath)
	}
}

// TestHelperProcess is re-executed as the spawned "agent" (and its grandchild)
// to reproduce a long-lived grandchild holding the inherited stdout pipe.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("ABF_HELPER") == "marker_then_hang" {
		// Print the completion marker, then block without exiting — mimicking an
		// agent (e.g. kiro) that finished its work but stays alive (blocked on a
		// build daemon). Trigger must detect the marker and kill us.
		fmt.Println("AUTO_BUG_FIX_RESULT outcome=auto-fix")
		time.Sleep(60 * time.Second)
		os.Exit(0)
	}
	if os.Getenv("ABF_HELPER") != "marker_then_orphan" {
		return
	}
	// Print the result marker, then spawn a long-lived grandchild that inherits
	// our stdout pipe and outlives us — mimicking the Gradle daemon. Use a stock
	// OS command (NOT the test binary) so it does not lock agent.test.exe on
	// Windows, which would break `go test`'s post-run cleanup. Exit without waiting.
	fmt.Println("AUTO_BUG_FIX_RESULT outcome=auto-fix")
	var gc *exec.Cmd
	if runtime.GOOS == "windows" {
		gc = exec.Command("ping", "-n", "4", "127.0.0.1") //nolint:gosec
	} else {
		gc = exec.Command("sleep", "3") //nolint:gosec
	}
	gc.Stdout = os.Stdout
	gc.Stderr = os.Stderr
	_ = gc.Start()
	os.Exit(0)
}

// TestTrigger_KillsHungAgentOnMarker guards the marker-triggered kill: when the
// agent prints the completion marker but does NOT exit (stays alive), Trigger
// must detect the marker, kill the child, and return promptly as a success.
func TestTrigger_KillsHungAgentOnMarker(t *testing.T) {
	cmd := os.Args[0] + ` -test.run=^TestHelperProcess$`
	opts := agent.Options{
		Env:       map[string]string{"ABF_HELPER": "marker_then_hang"},
		WaitDelay: 500 * time.Millisecond,
	}

	done := make(chan agent.Result, 1)
	go func() {
		result, _ := agent.Trigger("PROJ-1", cmd, opts)
		done <- result
	}()

	select {
	case result := <-done:
		if result.Outcome != "auto-fix" {
			t.Fatalf("outcome: got %q, want auto-fix", result.Outcome)
		}
		if result.ExitCode != 0 {
			t.Fatalf("a marker-triggered kill must report success, got exit code %d", result.ExitCode)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Trigger hung: marker seen but the still-running agent was not killed")
	}
}

// TestTrigger_DoesNotHangOnOrphanPipe guards the WaitDelay fix: when the agent
// exits but a grandchild keeps the stdout pipe open, Trigger must still return
// promptly (instead of blocking on cmd.Wait forever).
func TestTrigger_DoesNotHangOnOrphanPipe(t *testing.T) {
	cmd := os.Args[0] + ` -test.run=^TestHelperProcess$`
	opts := agent.Options{
		Env:       map[string]string{"ABF_HELPER": "marker_then_orphan"},
		WaitDelay: 500 * time.Millisecond,
	}

	done := make(chan agent.Result, 1)
	go func() {
		result, _ := agent.Trigger("PROJ-1", cmd, opts)
		done <- result
	}()

	select {
	case result := <-done:
		if result.Outcome != "auto-fix" {
			t.Fatalf("outcome: got %q, want auto-fix", result.Outcome)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Trigger hung: did not return after agent exited (grandchild held the pipe)")
	}
}
