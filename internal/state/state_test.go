package state_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fatecannotbealtered/auto-bug-fix/internal/state"
)

func TestSave_ConcurrentSavesStayValid(t *testing.T) {
	// The daemon saves from many goroutines at once (poll loop, per-issue poll
	// goroutines recording awaiting-approval, and the interaction listener). Saves
	// must not race on the temp file nor lose updates. Run with -race.
	path := filepath.Join(t.TempDir(), "state.json")
	s := state.New()
	var wg sync.WaitGroup
	for i := 0; i < 25; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("PROJ-%d", i)
			s.StartIssue(key, "cmd --fix "+key)
			if err := s.Save(path); err != nil {
				t.Errorf("save (start) %s: %v", key, err)
			}
			s.AddClarificationAnswer(key, "answer", "ou_x")
			s.FinishIssue(key, state.StatusDone, state.FinishDetails{Outcome: "auto-fix"})
			if err := s.Save(path); err != nil {
				t.Errorf("save (finish) %s: %v", key, err)
			}
		}(i)
	}
	wg.Wait()
	s2, err := state.Load(path)
	if err != nil {
		t.Fatalf("reload after concurrent saves must succeed (no corruption): %v", err)
	}
	if len(s2.Issues) == 0 {
		t.Fatal("expected issues to be persisted")
	}
}

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

func TestClarification_AccumulatesAndSurvivesReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := state.New()
	if s.ClarificationContext("PROJ-9") != "" {
		t.Fatal("empty clarification context should be empty string")
	}
	s.AddClarificationAnswer("PROJ-9", "环境是预发", "ou_alice")
	s.AddClarificationAnswer("PROJ-9", "只在 iOS 复现", "ou_alice")
	ctx := s.ClarificationContext("PROJ-9")
	if !strings.Contains(ctx, "环境是预发") || !strings.Contains(ctx, "只在 iOS 复现") {
		t.Fatalf("context should accumulate both answers, got %q", ctx)
	}
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	s2, err := state.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	c, ok := s2.Clarifications["PROJ-9"]
	if !ok || len(c.Rounds) != 2 {
		t.Fatalf("clarification should survive reload with 2 rounds, got %+v", c)
	}
	if c.Rounds[0].AnsweredBy != "ou_alice" {
		t.Fatalf("answeredBy not preserved: %+v", c.Rounds[0])
	}
}

func TestLoad_LegacyFileWithoutClarificationsMap(t *testing.T) {
	// A state.json written before the clarifications field must still load with a
	// usable (non-nil) map so the daemon can record answers.
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"lastPollAt":"0001-01-01T00:00:00Z","issues":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := state.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Clarifications == nil {
		t.Fatal("Clarifications map should be initialized for a legacy file")
	}
	s.AddClarificationAnswer("PROJ-1", "answer", "ou_x") // must not panic on nil map
}

func TestPendingApproval_RoundTripAndSurvivesReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := state.New()
	if _, ok := s.GetPendingApproval("PROJ-5"); ok {
		t.Fatal("no approval should exist yet")
	}
	s.SetPendingApproval(state.PendingApproval{
		IssueKey: "PROJ-5", Workspace: "/ws/repo", FixBranch: "fix/PROJ-5-x",
		BaseBranch: "develop", HeadSHA: "abc123",
	})
	got, ok := s.GetPendingApproval("PROJ-5")
	if !ok || got.HeadSHA != "abc123" || got.FixBranch != "fix/PROJ-5-x" {
		t.Fatalf("pending approval not stored: %+v ok=%v", got, ok)
	}
	if got.CreatedAt.IsZero() {
		t.Fatal("CreatedAt should be stamped")
	}
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	s2, err := state.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s2.GetPendingApproval("PROJ-5"); !ok {
		t.Fatal("pending approval should survive reload")
	}
	s2.DeletePendingApproval("PROJ-5")
	if _, ok := s2.GetPendingApproval("PROJ-5"); ok {
		t.Fatal("pending approval should be deletable")
	}
}

func TestClaimForRunAndPoll_MutuallyExclusive(t *testing.T) {
	// The listener claim and the poll claim must never both succeed for one issue.
	s := state.New()
	if !s.TryClaimForRun("PROJ-1") {
		t.Fatal("first listener claim should succeed on a fresh issue")
	}
	if s.Issues["PROJ-1"].Status != state.StatusTriggered {
		t.Fatalf("TryClaimForRun should mark triggered, got %q", s.Issues["PROJ-1"].Status)
	}
	if s.Issues["PROJ-1"].Attempts != 0 {
		t.Fatalf("TryClaimForRun must not increment Attempts, got %d", s.Issues["PROJ-1"].Attempts)
	}
	// Poll must not claim it now (already triggered).
	if s.ClaimForPoll("PROJ-1", 365, "cmd") {
		t.Fatal("poll must not claim an issue a listener already claimed")
	}
	// A second listener claim must also fail.
	if s.TryClaimForRun("PROJ-1") {
		t.Fatal("a second listener claim must fail while triggered")
	}
}

func TestTryClaimForRun_BlockedByAwaitingApproval(t *testing.T) {
	s := state.New()
	s.FinishIssue("PROJ-2", state.StatusAwaitingApproval, state.FinishDetails{Outcome: "auto-fix"})
	if s.TryClaimForRun("PROJ-2") {
		t.Fatal("a rerun/answer must not claim an issue that is awaiting approval")
	}
}

func TestClaimForPoll_RecordsRunAtomically(t *testing.T) {
	s := state.New()
	if !s.ClaimForPoll("PROJ-3", 0, "agent --fix PROJ-3") {
		t.Fatal("poll should claim a fresh issue")
	}
	e := s.Issues["PROJ-3"]
	if e.Status != state.StatusTriggered || e.Attempts != 1 || e.AgentCommand == "" {
		t.Fatalf("ClaimForPoll should record a started run, got %+v", e)
	}
}

func TestSweepHelpers_ExpireApprovalsAndPruneClarifications(t *testing.T) {
	s := state.New()
	now := time.Now()
	// fresh approval + stale approval
	s.SetPendingApproval(state.PendingApproval{IssueKey: "FRESH", HeadSHA: "a"})
	s.Approvals["FRESH"].CreatedAt = now
	s.SetPendingApproval(state.PendingApproval{IssueKey: "STALE", HeadSHA: "b"})
	s.Approvals["STALE"].CreatedAt = now.Add(-48 * time.Hour)

	cutoff := now.Add(-24 * time.Hour)
	expired := s.ExpiredApprovalKeys(cutoff)
	if len(expired) != 1 || expired[0] != "STALE" {
		t.Fatalf("expected only STALE expired, got %v", expired)
	}

	s.AddClarificationAnswer("OLD", "a", "ou_x")
	s.Clarifications["OLD"].UpdatedAt = now.Add(-48 * time.Hour)
	s.AddClarificationAnswer("NEW", "a", "ou_x") // UpdatedAt = now
	if n := s.PruneClarifications(cutoff); n != 1 {
		t.Fatalf("expected 1 pruned clarification, got %d", n)
	}
	if _, ok := s.Clarifications["OLD"]; ok {
		t.Fatal("OLD clarification should be pruned")
	}
	if _, ok := s.Clarifications["NEW"]; !ok {
		t.Fatal("NEW clarification should survive")
	}
}

func TestShouldTrigger_AwaitingApprovalNeverRetriggers(t *testing.T) {
	s := state.New()
	s.FinishIssue("PROJ-6", state.StatusAwaitingApproval, state.FinishDetails{Outcome: "auto-fix"})
	// even with a generous expiry window, a pending approval must not re-trigger
	if s.ShouldTrigger("PROJ-6", 365) {
		t.Fatal("awaiting-approval must never be re-triggered by the poller")
	}
}

func TestLoad_LegacyFileWithoutApprovalsMap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"issues":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := state.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Approvals == nil {
		t.Fatal("Approvals map should be initialized for a legacy file")
	}
	s.SetPendingApproval(state.PendingApproval{IssueKey: "PROJ-1"}) // must not panic
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
