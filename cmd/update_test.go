package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// captureUpdate runs fn (a non-exiting update path) with a controllable command
// runner and a fixed compact JSON output, returning the parsed envelope written
// to stdout. Paths that call fail() (os.Exit) are covered by the subprocess
// tests below, not here.
func captureUpdate(t *testing.T, runner func(ctx context.Context, name string, args ...string) error, fn func()) jsonEnvelope {
	t.Helper()
	origRunner := updateRunCommand
	origOut := activeOutput
	updateRunCommand = runner
	activeOutput = outputOptions{Format: "json", Compact: true}
	t.Cleanup(func() {
		updateRunCommand = origRunner
		activeOutput = origOut
	})

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	origStdout := os.Stdout
	os.Stdout = w
	fn()
	_ = w.Close()
	os.Stdout = origStdout
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)

	var env jsonEnvelope
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("update output is not one JSON envelope: %v\n%s", err, buf.String())
	}
	return env
}

// TestUpdateExecutesWithoutConfirmToken: a bare `update` (no --confirm) runs the
// whole npm update + skill sync in one call and returns ok with binary_replaced.
func TestUpdateExecutesWithoutConfirmToken(t *testing.T) {
	var calls []string
	runner := func(_ context.Context, name string, args ...string) error {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return nil
	}
	env := captureUpdate(t, runner, func() {
		runUpdate([]string{"--target-version", "9.9.9"})
	})
	if !env.OK {
		t.Fatalf("bare update should succeed: %+v", env.Error)
	}
	data, _ := env.Data.(map[string]any)
	if status, _ := data["status"].(string); status != "updated" {
		t.Fatalf("status should be updated: %#v", data)
	}
	if replaced, _ := data["binary_replaced"].(bool); !replaced {
		t.Fatalf("binary_replaced should be true on success: %#v", data)
	}
	if data["current_version"] != "9.9.9" {
		t.Fatalf("current_version should advance to target: %#v", data)
	}
	if len(calls) != 2 || !strings.HasPrefix(calls[0], "npm install") || !strings.HasPrefix(calls[1], "npx skills") {
		t.Fatalf("expected npm install then npx skills add, got %v", calls)
	}
}

// TestUpdateIdempotentNoOp: already at the requested version returns ok with a
// no-op result and never invokes npm/npx.
func TestUpdateIdempotentNoOp(t *testing.T) {
	called := false
	runner := func(_ context.Context, name string, args ...string) error {
		called = true
		return nil
	}
	env := captureUpdate(t, runner, func() {
		runUpdate([]string{"--target-version", normalizeVersion(version)})
	})
	if !env.OK {
		t.Fatalf("idempotent update should succeed: %+v", env.Error)
	}
	if called {
		t.Fatal("idempotent update must not run npm/npx")
	}
	data, _ := env.Data.(map[string]any)
	if status, _ := data["status"].(string); status != "up_to_date" {
		t.Fatalf("idempotent status should be up_to_date: %#v", data)
	}
}

// failClassify exercises the pure classification helpers without os.Exit.
func TestUpdateFailureDetailsCarryStageInvariant(t *testing.T) {
	d := updateFailureDetails(stageReplace, "1.2.3", false, "not_started")
	for _, k := range []string{"stage", "current_version", "binary_replaced", "skill_sync_status"} {
		if _, ok := d[k]; !ok {
			t.Errorf("failure details missing %q: %#v", k, d)
		}
	}
}

func TestIsPermissionError(t *testing.T) {
	if !isPermissionError(os.ErrPermission) {
		t.Error("os.ErrPermission should classify as permission error")
	}
	if isPermissionError(errors.New("boom")) {
		t.Error("a generic error must not classify as permission error")
	}
}

// --- subprocess tests for paths that exit via fail() ---

// TestUpdateSkillSyncPartialSuccess: npm succeeds, npx fails -> partial success
// (ok:false, binary_replaced:true, retryable, skill_sync_command present), exit 7.
func TestUpdateSkillSyncPartialSuccess(t *testing.T) {
	env, exit := runUpdateSubprocess(t, "skill_sync_fail", "--target-version", "9.9.9", "--compact")
	if env.OK {
		t.Fatalf("skill_sync failure must be ok:false (partial success): %+v", env)
	}
	if env.Error == nil || env.Error.Code != "E_NETWORK" || !env.Error.Retryable {
		t.Fatalf("skill_sync failure should be retryable E_NETWORK: %+v", env.Error)
	}
	if exit != exitNetwork {
		t.Fatalf("skill_sync failure exit want %d got %d", exitNetwork, exit)
	}
	d := env.Error.Details
	if br, _ := d["binary_replaced"].(bool); !br {
		t.Fatalf("partial success must report binary_replaced:true: %#v", d)
	}
	if d["stage"] != "skill_sync" {
		t.Fatalf("stage should be skill_sync: %#v", d)
	}
	if _, ok := d["skill_sync_command"]; !ok {
		t.Fatalf("partial success must carry skill_sync_command: %#v", d)
	}
}

// TestUpdateReplaceFailureIsE_IO: npm install fails (not permission) -> E_IO,
// exit 1, non-retryable, binary_replaced:false.
func TestUpdateReplaceFailureIsE_IO(t *testing.T) {
	env, exit := runUpdateSubprocess(t, "replace_fail", "--target-version", "9.9.9", "--compact")
	if env.OK {
		t.Fatalf("replace failure must fail: %+v", env)
	}
	if env.Error == nil || env.Error.Code != "E_IO" || env.Error.Retryable {
		t.Fatalf("replace io failure should be non-retryable E_IO: %+v", env.Error)
	}
	if exit != exitGeneric {
		t.Fatalf("E_IO exit want 1 got %d", exit)
	}
	if br, _ := env.Error.Details["binary_replaced"].(bool); br {
		t.Fatalf("replace failure must report binary_replaced:false: %#v", env.Error.Details)
	}
}

// runUpdateSubprocess re-executes this test binary with an env hook that swaps
// updateRunCommand for a scripted failure, captures stdout + exit code.
func runUpdateSubprocess(t *testing.T, mode string, args ...string) (jsonEnvelope, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run", "TestUpdateSubprocessHook") //nolint:gosec
	cmd.Env = append(os.Environ(),
		"AUTO_BUG_FIX_TEST_UPDATE_HOOK="+mode,
		"AUTO_BUG_FIX_TEST_UPDATE_ARGS="+strings.Join(args, " "),
	)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	err := cmd.Run()
	exit := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		exit = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("subprocess run: %v", err)
	}
	var env jsonEnvelope
	if jerr := json.Unmarshal(stdout.Bytes(), &env); jerr != nil {
		t.Fatalf("subprocess stdout is not one JSON envelope: %v\n%s", jerr, stdout.String())
	}
	return env, exit
}

// TestUpdateSubprocessHook is the child entrypoint: when the env hook is set it
// installs a scripted runner and runs runUpdate (which exits via fail()).
func TestUpdateSubprocessHook(t *testing.T) {
	mode := os.Getenv("AUTO_BUG_FIX_TEST_UPDATE_HOOK")
	if mode == "" {
		t.Skip("not a subprocess invocation")
	}
	// activeOutput is set directly, so strip the output flag the parent appended
	// (runUpdate's own parser rejects unknown args like --compact).
	var args []string
	for _, a := range strings.Fields(os.Getenv("AUTO_BUG_FIX_TEST_UPDATE_ARGS")) {
		if a == "--compact" {
			continue
		}
		args = append(args, a)
	}
	activeOutput = outputOptions{Format: "json", Compact: true}
	updateRunCommand = func(_ context.Context, name string, _ ...string) error {
		switch {
		case mode == "replace_fail" && name == "npm":
			return errors.New("ENOSPC: no space left on device")
		case mode == "skill_sync_fail" && name == "npx":
			return errors.New("npx: command not found")
		default:
			return nil
		}
	}
	runUpdate(args)
}
