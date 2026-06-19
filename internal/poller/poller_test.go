package poller_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/fatecannotbealtered/auto-bug-fix/internal/agent"
	"github.com/fatecannotbealtered/auto-bug-fix/internal/config"
	"github.com/fatecannotbealtered/auto-bug-fix/internal/poller"
	"github.com/fatecannotbealtered/auto-bug-fix/internal/state"
)

type fakeJira struct {
	keys []string
	err  error
}

func (f *fakeJira) ListIssues(_ config.FilterConfig) ([]string, error) {
	return f.keys, f.err
}

type fakeTrigger struct {
	mu        sync.Mutex
	triggered []string
	err       error
	outcome   string // defaults to auto-fix when empty
}

func (f *fakeTrigger) Command() string {
	return "fake-agent --fix {issueKey}"
}

func (f *fakeTrigger) Trigger(issueKey string) (agent.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.triggered = append(f.triggered, issueKey)
	if f.err != nil {
		return agent.Result{ExitCode: 1, DurationMillis: 10}, f.err
	}
	outcome := f.outcome
	if outcome == "" {
		outcome = agent.OutcomeAutoFix
	}
	return agent.Result{Outcome: outcome, MRURL: "https://gitlab.example/mr/1", HandoffPath: ".repo-knowledge/handoff/PROJ-1.needs-confirmation.md", ExitCode: 0, DurationMillis: 10}, nil
}

var emptyFilter = config.FilterConfig{}

func TestPollOnce_TriggersNewIssues(t *testing.T) {
	jira := &fakeJira{keys: []string{"PROJ-1", "PROJ-2"}}
	trigger := &fakeTrigger{}
	st := state.New()

	if err := poller.PollOnce(jira, trigger, st, emptyFilter, 3, 0); err != nil {
		t.Fatal(err)
	}
	if len(trigger.triggered) != 2 {
		t.Fatalf("expected 2 triggered, got %d", len(trigger.triggered))
	}
	if st.Issues["PROJ-1"].AgentCommand == "" {
		t.Fatal("expected agent command recorded in state")
	}
	if st.Issues["PROJ-1"].Outcome != "auto-fix" {
		t.Fatalf("expected outcome recorded, got %q", st.Issues["PROJ-1"].Outcome)
	}
	if st.Issues["PROJ-1"].HandoffPath == "" {
		t.Fatal("expected handoff path recorded in state")
	}
}

func TestPollOnce_SkipsKnownIssues(t *testing.T) {
	jira := &fakeJira{keys: []string{"PROJ-1", "PROJ-2"}}
	trigger := &fakeTrigger{}
	st := state.New()
	st.SetIssue("PROJ-1", state.StatusTriggered)

	if err := poller.PollOnce(jira, trigger, st, emptyFilter, 3, 0); err != nil {
		t.Fatal(err)
	}
	if len(trigger.triggered) != 1 || trigger.triggered[0] != "PROJ-2" {
		t.Fatalf("expected only PROJ-2 triggered, got %v", trigger.triggered)
	}
}

func TestPollOnce_DoesNotRetriggerDoneIssueWhenExpiryZero(t *testing.T) {
	jira := &fakeJira{keys: []string{"PROJ-1"}}
	trigger := &fakeTrigger{}
	st := state.New()
	st.SetIssue("PROJ-1", state.StatusDone)
	st.Issues["PROJ-1"].TriggeredAt = time.Now().Add(-100 * 24 * time.Hour)

	if err := poller.PollOnce(jira, trigger, st, emptyFilter, 3, 0); err != nil {
		t.Fatal(err)
	}
	if len(trigger.triggered) != 0 {
		t.Fatalf("expected no trigger, got %v", trigger.triggered)
	}
}

func TestPollOnce_RecordsOutcomeStatus(t *testing.T) {
	cases := []struct {
		outcome string
		want    state.Status
	}{
		{agent.OutcomeAutoFix, state.StatusDone},
		{agent.OutcomeNeedsInfo, state.StatusWaiting},
		{agent.OutcomeAutoDiagnose, state.StatusWaiting},
		{"", state.StatusDone}, // no marker printed
	}
	for _, tc := range cases {
		t.Run(tc.outcome, func(t *testing.T) {
			jira := &fakeJira{keys: []string{"PROJ-1"}}
			trigger := &fakeTrigger{outcome: tc.outcome}
			st := state.New()

			if err := poller.PollOnce(jira, trigger, st, emptyFilter, 3, 0); err != nil {
				t.Fatal(err)
			}
			if got := st.Issues["PROJ-1"].Status; got != tc.want {
				t.Fatalf("outcome %q: status got %q, want %q", tc.outcome, got, tc.want)
			}
		})
	}
}

func TestPollOnce_JiraError(t *testing.T) {
	jira := &fakeJira{err: fmt.Errorf("jira down")}
	trigger := &fakeTrigger{}
	st := state.New()

	if err := poller.PollOnce(jira, trigger, st, emptyFilter, 3, 0); err == nil {
		t.Fatal("expected error from jira")
	}
}

func TestPollOnce_RecordsTriggerFailure(t *testing.T) {
	jira := &fakeJira{keys: []string{"PROJ-1"}}
	trigger := &fakeTrigger{err: fmt.Errorf("agent failed")}
	st := state.New()

	if err := poller.PollOnce(jira, trigger, st, emptyFilter, 3, 0); err != nil {
		t.Fatal(err)
	}
	entry := st.Issues["PROJ-1"]
	if entry.Status != state.StatusFailed {
		t.Fatalf("status: got %q, want %q", entry.Status, state.StatusFailed)
	}
	if entry.LastError == "" {
		t.Fatal("expected last error recorded")
	}
	if entry.ExitCode != 1 {
		t.Fatalf("exit code: got %d, want 1", entry.ExitCode)
	}
}

func TestPollOnce_ConcurrentTrigger(t *testing.T) {
	keys := make([]string, 10)
	for i := range keys {
		keys[i] = fmt.Sprintf("PROJ-%d", i)
	}
	jira := &fakeJira{keys: keys}
	trigger := &fakeTrigger{}
	st := state.New()

	if err := poller.PollOnce(jira, trigger, st, emptyFilter, 3, 0); err != nil {
		t.Fatal(err)
	}
	if len(trigger.triggered) != 10 {
		t.Fatalf("expected 10 triggered, got %d", len(trigger.triggered))
	}
}

func TestRun_StopsOnContextCancel(t *testing.T) {
	jira := &fakeJira{keys: []string{}}
	trigger := &fakeTrigger{}
	st := state.New()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		poller.Run(ctx, jira, trigger, st, emptyFilter, 50*time.Millisecond, "", 3, 0)
		close(done)
	}()

	time.Sleep(120 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("poller did not stop after context cancel")
	}
}

func TestParseJiraKeys_ValidJSON(t *testing.T) {
	data, _ := json.Marshal(map[string]any{
		"issues": []map[string]any{
			{"key": "PROJ-1"},
			{"key": "PROJ-2"},
		},
	})
	keys, err := poller.ParseJiraKeysForTest(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0] != "PROJ-1" {
		t.Fatalf("got %v", keys)
	}
}

func TestParseJiraKeys_EnvelopeJSON(t *testing.T) {
	data, _ := json.Marshal(map[string]any{
		"ok":             true,
		"schema_version": "1.0",
		"data": map[string]any{
			"issues": []map[string]any{
				{"key": "PROJ-1"},
				{"key": "PROJ-2"},
			},
		},
	})
	keys, err := poller.ParseJiraKeysForTest(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0] != "PROJ-1" || keys[1] != "PROJ-2" {
		t.Fatalf("got %v", keys)
	}
}

func TestParseJiraKeys_ErrorEnvelope(t *testing.T) {
	data, _ := json.Marshal(map[string]any{
		"ok":             false,
		"schema_version": "1.0",
		"error": map[string]any{
			"code":    "E_VALIDATION",
			"message": "bad jql",
		},
	})
	_, err := poller.ParseJiraKeysForTest(data)
	if err == nil {
		t.Fatal("expected error for ok:false envelope")
	}
	if got := err.Error(); got != "E_VALIDATION: bad jql" {
		t.Fatalf("error = %q", got)
	}
}

func TestParseJiraKeys_InvalidJSON(t *testing.T) {
	_, err := poller.ParseJiraKeysForTest([]byte("{bad}"))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestPollOnce_RetriggersInterrupted(t *testing.T) {
	st := state.New()
	st.Issues["BUG-1"] = &state.IssueEntry{Status: state.StatusInterrupted}

	lister := &fakeJira{keys: []string{"BUG-1"}}
	trig := &fakeTrigger{}

	if err := poller.PollOnce(lister, trig, st, emptyFilter, 3, 0); err != nil {
		t.Fatal(err)
	}
	if len(trig.triggered) != 1 {
		t.Fatalf("expected interrupted issue to be retriggered once, got %d calls", len(trig.triggered))
	}
}
