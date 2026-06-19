package state_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/fatecannotbealtered/auto-bug-fix/internal/state"
)

func TestLoadSave_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := state.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Issues) != 0 {
		t.Fatal("expected empty issues on first load")
	}

	s.SetIssue("PROJ-1", state.StatusTriggered)
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}

	s2, err := state.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := s2.Issues["PROJ-1"]
	if !ok {
		t.Fatal("PROJ-1 not found after reload")
	}
	if entry.Status != state.StatusTriggered {
		t.Fatalf("got status %q, want %q", entry.Status, state.StatusTriggered)
	}
}

func TestLoad_MissingFileReturnsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nonexistent.json")
	s, err := state.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Issues == nil {
		t.Fatal("Issues map should be initialized")
	}
}

func TestSetIssue_UpdatesTimestamp(t *testing.T) {
	s := state.New()
	before := time.Now()
	s.SetIssue("PROJ-1", state.StatusDone)
	after := time.Now()

	entry := s.Issues["PROJ-1"]
	if entry.TriggeredAt.Before(before) || entry.TriggeredAt.After(after) {
		t.Fatal("TriggeredAt not set correctly")
	}
}

func TestStartAndFinishIssue_RecordsAuditFields(t *testing.T) {
	s := state.New()
	s.StartIssue("PROJ-1", "agent --fix {issueKey}")
	s.FinishIssue("PROJ-1", state.StatusDone, state.FinishDetails{
		Outcome:        "auto-fix",
		MRURL:          "https://gitlab.example/mr/1",
		HandoffPath:    ".repo-knowledge/handoff/PROJ-1.needs-confirmation.md",
		ExitCode:       0,
		DurationMillis: 42,
	})

	entry := s.Issues["PROJ-1"]
	if entry.AgentCommand != "agent --fix {issueKey}" {
		t.Fatalf("agent command not recorded: %q", entry.AgentCommand)
	}
	if entry.Attempts != 1 {
		t.Fatalf("attempts: got %d, want 1", entry.Attempts)
	}
	if entry.Outcome != "auto-fix" || entry.MRURL == "" {
		t.Fatalf("outcome audit not recorded: %+v", entry)
	}
	if entry.HandoffPath != ".repo-knowledge/handoff/PROJ-1.needs-confirmation.md" {
		t.Fatalf("handoff path not recorded: %+v", entry)
	}
	if entry.DurationMillis != 42 {
		t.Fatalf("duration: got %d, want 42", entry.DurationMillis)
	}
	if entry.CompletedAt.IsZero() {
		t.Fatal("completedAt should be recorded")
	}
}

func TestStartIssueRedactsSecretLikeCommandArgs(t *testing.T) {
	s := state.New()
	s.StartIssue("PROJ-1", "agent --token abc --password=def --safe ok")

	entry := s.Issues["PROJ-1"]
	if entry.AgentCommand != "agent --token <redacted> --password=<redacted> --safe ok" {
		t.Fatalf("secret args were not redacted: %q", entry.AgentCommand)
	}
}

func TestStatusConstants(t *testing.T) {
	for _, st := range []state.Status{state.StatusTriggered, state.StatusDone, state.StatusFailed, state.StatusInterrupted, state.StatusWaiting} {
		if st == "" {
			t.Fatalf("status constant should not be empty")
		}
	}
}

func TestReclaimStaleTriggered(t *testing.T) {
	s := state.New()
	s.StartIssue("A-1", "cmd")                                    // triggered
	s.FinishIssue("A-2", state.StatusDone, state.FinishDetails{}) // done
	s.FinishIssue("A-3", state.StatusFailed, state.FinishDetails{})

	n := s.ReclaimStaleTriggered()
	if n != 1 {
		t.Fatalf("expected 1 reclaimed, got %d", n)
	}
	if s.Issues["A-1"].Status != state.StatusInterrupted {
		t.Errorf("A-1 should be interrupted, got %s", s.Issues["A-1"].Status)
	}
	if s.Issues["A-2"].Status != state.StatusDone {
		t.Errorf("A-2 should stay done")
	}
}

func TestShouldTrigger(t *testing.T) {
	s := state.New()
	if !s.ShouldTrigger("UNKNOWN-1", 0) {
		t.Error("unknown issue should trigger")
	}

	s.StartIssue("RUN-1", "cmd") // triggered
	if s.ShouldTrigger("RUN-1", 0) {
		t.Error("triggered (running) issue should not trigger")
	}

	s.Issues["INT-1"] = &state.IssueEntry{Status: state.StatusInterrupted}
	if !s.ShouldTrigger("INT-1", 0) {
		t.Error("interrupted issue should re-trigger")
	}

	s.FinishIssue("DONE-1", state.StatusDone, state.FinishDetails{})
	if s.ShouldTrigger("DONE-1", 0) {
		t.Error("done issue with expiry 0 should not trigger")
	}

	s.Issues["OLD-DONE"] = &state.IssueEntry{Status: state.StatusDone, CompletedAt: time.Now().Add(-48 * time.Hour)}
	if !s.ShouldTrigger("OLD-DONE", 1) {
		t.Error("done issue older than expiry should re-trigger")
	}

	// waiting (needs-info / auto-diagnose) follows the same expiry rule as done.
	s.FinishIssue("WAIT-1", state.StatusWaiting, state.FinishDetails{})
	if s.ShouldTrigger("WAIT-1", 0) {
		t.Error("waiting issue with expiry 0 should not re-trigger (would spam the ticket)")
	}
	s.Issues["OLD-WAIT"] = &state.IssueEntry{Status: state.StatusWaiting, CompletedAt: time.Now().Add(-48 * time.Hour)}
	if !s.ShouldTrigger("OLD-WAIT", 1) {
		t.Error("waiting issue older than expiry should re-trigger")
	}
}
