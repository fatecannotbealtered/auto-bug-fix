package listener_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fatecannotbealtered/auto-bug-fix/internal/agent"
	"github.com/fatecannotbealtered/auto-bug-fix/internal/listener"
	"github.com/fatecannotbealtered/auto-bug-fix/internal/notify"
	"github.com/fatecannotbealtered/auto-bug-fix/internal/state"
)

type fakeConsumer struct{ events []notify.CallbackEvent }

func (f fakeConsumer) Consume(_ context.Context, handle func(notify.CallbackEvent) error) error {
	for _, e := range f.events {
		_ = handle(e)
	}
	return nil
}

type call struct{ issue, ctx string }

type fakeRetrigger struct{ got chan call }

func (r *fakeRetrigger) Retrigger(issueKey, humanContext string) (agent.Result, error) {
	r.got <- call{issueKey, humanContext}
	return agent.Result{Outcome: agent.OutcomeAutoFix}, nil
}

type fakeApprover struct {
	approved chan string
	rejected chan call // reuse: {issue, ctx=reason}
}

func (a *fakeApprover) Approve(issueKey string) (agent.Result, error) {
	a.approved <- issueKey
	return agent.Result{Outcome: agent.OutcomeAutoFix, MRURL: "https://x/mr/1"}, nil
}

func (a *fakeApprover) Reject(issueKey, reason string) error {
	a.rejected <- call{issueKey, reason}
	return nil
}

type fakeController struct {
	pauses  chan struct{}
	resumes chan struct{}
	reruns  chan string
	status  chan string
}

func (c *fakeController) Pause()  { c.pauses <- struct{}{} }
func (c *fakeController) Resume() { c.resumes <- struct{}{} }
func (c *fakeController) Rerun(issueKey string) (agent.Result, error) {
	c.reruns <- issueKey
	return agent.Result{Outcome: agent.OutcomeAutoFix}, nil
}
func (c *fakeController) SendStatus(toOpenID string) { c.status <- toOpenID }

func newListener(t *testing.T, d listener.Deps, events ...notify.CallbackEvent) (*listener.Listener, *state.State) {
	t.Helper()
	st := state.New()
	d.State = st
	d.StatePath = filepath.Join(t.TempDir(), "state.json")
	d.Consumer = fakeConsumer{events: events}
	return listener.New(d), st
}

func TestListener_AuthorizedAnswerRecordsAndRetriggers(t *testing.T) {
	rt := &fakeRetrigger{got: make(chan call, 4)}
	ev := notify.CallbackEvent{EventID: "e1", OperatorID: "ou_alice", Action: "answer", Issue: "PROJ-1", Answer: "环境是预发"}
	l, st := newListener(t, listener.Deps{Retrigger: rt, AuthorizedOpenIDs: []string{"ou_alice"}}, ev)

	_ = l.Run(context.Background())

	select {
	case c := <-rt.got:
		if c.issue != "PROJ-1" {
			t.Fatalf("re-trigger issue: got %q", c.issue)
		}
		if !strings.Contains(c.ctx, "环境是预发") {
			t.Fatalf("re-trigger should carry the answer as context, got %q", c.ctx)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected a re-trigger for an authorized answer")
	}
	if got := st.ClarificationContext("PROJ-1"); !strings.Contains(got, "环境是预发") {
		t.Fatalf("answer should be recorded, got %q", got)
	}
}

func TestListener_UnauthorizedOperatorIgnored(t *testing.T) {
	rt := &fakeRetrigger{got: make(chan call, 4)}
	ev := notify.CallbackEvent{EventID: "e2", OperatorID: "ou_evil", Action: "answer", Issue: "PROJ-1", Answer: "恶意"}
	l, st := newListener(t, listener.Deps{Retrigger: rt, AuthorizedOpenIDs: []string{"ou_alice"}}, ev)

	_ = l.Run(context.Background())

	if got := st.ClarificationContext("PROJ-1"); got != "" {
		t.Fatalf("unauthorized answer must not be recorded, got %q", got)
	}
	select {
	case <-rt.got:
		t.Fatal("unauthorized operator must not re-trigger a fix")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestListener_DedupsByEventID(t *testing.T) {
	rt := &fakeRetrigger{got: make(chan call, 4)}
	ev := notify.CallbackEvent{EventID: "dup", OperatorID: "ou_alice", Action: "answer", Issue: "PROJ-1", Answer: "一次"}
	l, _ := newListener(t, listener.Deps{Retrigger: rt, AuthorizedOpenIDs: []string{"ou_alice"}}, ev, ev) // same event twice

	_ = l.Run(context.Background())

	<-rt.got // first delivered
	select {
	case <-rt.got:
		t.Fatal("a duplicate event_id must not re-trigger a second time")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestListener_ApproveResumesExecute(t *testing.T) {
	ap := &fakeApprover{approved: make(chan string, 2), rejected: make(chan call, 2)}
	ev := notify.CallbackEvent{EventID: "a1", OperatorID: "ou_alice", Action: "approve", Issue: "PROJ-2"}
	l, _ := newListener(t, listener.Deps{Approver: ap, AuthorizedOpenIDs: []string{"ou_alice"}}, ev)
	_ = l.Run(context.Background())
	select {
	case got := <-ap.approved:
		if got != "PROJ-2" {
			t.Fatalf("approve issue: got %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("approve must call Approver.Approve")
	}
}

func TestListener_RejectPassesReason(t *testing.T) {
	ap := &fakeApprover{approved: make(chan string, 2), rejected: make(chan call, 2)}
	ev := notify.CallbackEvent{EventID: "r1", OperatorID: "ou_alice", Action: "reject", Issue: "PROJ-2", Fields: map[string]string{"reason": "方案不对"}}
	l, _ := newListener(t, listener.Deps{Approver: ap, AuthorizedOpenIDs: []string{"ou_alice"}}, ev)
	_ = l.Run(context.Background())
	select {
	case got := <-ap.rejected:
		if got.issue != "PROJ-2" || got.ctx != "方案不对" {
			t.Fatalf("reject: got %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reject must call Approver.Reject with the reason")
	}
}

func TestListener_RerunUsesTypedIssueKey(t *testing.T) {
	ctl := &fakeController{reruns: make(chan string, 2), pauses: make(chan struct{}, 2), resumes: make(chan struct{}, 2), status: make(chan string, 2)}
	ev := notify.CallbackEvent{EventID: "c1", OperatorID: "ou_alice", Action: "rerun", Fields: map[string]string{"issueKey": "PROJ-9"}}
	l, _ := newListener(t, listener.Deps{Controller: ctl, AuthorizedOpenIDs: []string{"ou_alice"}}, ev)
	_ = l.Run(context.Background())
	select {
	case got := <-ctl.reruns:
		if got != "PROJ-9" {
			t.Fatalf("rerun issue: got %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("rerun must call Controller.Rerun with the typed key")
	}
}

func TestListener_PauseFlipsAndRefreshes(t *testing.T) {
	ctl := &fakeController{reruns: make(chan string, 2), pauses: make(chan struct{}, 2), resumes: make(chan struct{}, 2), status: make(chan string, 2)}
	ev := notify.CallbackEvent{EventID: "p1", OperatorID: "ou_alice", Action: "pause"}
	l, _ := newListener(t, listener.Deps{Controller: ctl, AuthorizedOpenIDs: []string{"ou_alice"}}, ev)
	_ = l.Run(context.Background())
	select {
	case <-ctl.pauses:
	case <-time.After(time.Second):
		t.Fatal("pause must call Controller.Pause")
	}
	select {
	case <-ctl.status:
	case <-time.After(time.Second):
		t.Fatal("pause should refresh the control card via SendStatus")
	}
}

type blockingApprover struct {
	approved chan string
	rejected chan string
	gate     chan struct{}
}

func (a *blockingApprover) Approve(issueKey string) (agent.Result, error) {
	a.approved <- issueKey
	<-a.gate // hold the in-flight slot until released
	return agent.Result{Outcome: agent.OutcomeAutoFix, MRURL: "https://x/mr/1"}, nil
}
func (a *blockingApprover) Reject(issueKey, reason string) error {
	a.rejected <- issueKey
	return nil
}

func TestListener_RejectIgnoredWhileApproveInFlight(t *testing.T) {
	ap := &blockingApprover{approved: make(chan string, 1), rejected: make(chan string, 1), gate: make(chan struct{})}
	evApprove := notify.CallbackEvent{EventID: "a", OperatorID: "ou_alice", Action: "approve", Issue: "PROJ-2"}
	evReject := notify.CallbackEvent{EventID: "b", OperatorID: "ou_alice", Action: "reject", Issue: "PROJ-2"}
	l, _ := newListener(t, listener.Deps{Approver: ap, AuthorizedOpenIDs: []string{"ou_alice"}}, evApprove, evReject)
	go func() { _ = l.Run(context.Background()) }()

	select {
	case <-ap.approved:
	case <-time.After(2 * time.Second):
		t.Fatal("approve should have started")
	}
	// The reject event was dispatched after approve; while approve holds inFlight it
	// must NOT run (else it would race the in-flight execute / open an MR for a
	// rejected fix).
	select {
	case <-ap.rejected:
		t.Fatal("reject must be ignored while an approve is in flight for the same issue")
	case <-time.After(250 * time.Millisecond):
	}
	close(ap.gate) // release approve
}

func TestListener_AnswerMarksTriggeredSynchronously(t *testing.T) {
	rt := &fakeRetrigger{got: make(chan call, 2)}
	ev := notify.CallbackEvent{EventID: "m1", OperatorID: "ou_alice", Action: "answer", Issue: "PROJ-1", Answer: "补充"}
	l, st := newListener(t, listener.Deps{Retrigger: rt, AuthorizedOpenIDs: []string{"ou_alice"}}, ev)
	_ = l.Run(context.Background())
	// MarkTriggered runs synchronously in handle(), so by the time Run returns the
	// issue is triggered and a concurrent poll would skip it.
	if e := st.Issues["PROJ-1"]; e == nil || e.Status != state.StatusTriggered {
		t.Fatalf("answer should mark the issue triggered synchronously, got %+v", e)
	}
	<-rt.got // drain the async re-trigger
}

func TestListener_UnauthorizedControlIgnored(t *testing.T) {
	ctl := &fakeController{reruns: make(chan string, 2), pauses: make(chan struct{}, 2), resumes: make(chan struct{}, 2), status: make(chan string, 2)}
	ev := notify.CallbackEvent{EventID: "u1", OperatorID: "ou_evil", Action: "pause"}
	l, _ := newListener(t, listener.Deps{Controller: ctl, AuthorizedOpenIDs: []string{"ou_alice"}}, ev)
	_ = l.Run(context.Background())
	select {
	case <-ctl.pauses:
		t.Fatal("an unauthorized operator must not control the poller")
	case <-time.After(200 * time.Millisecond):
	}
}
