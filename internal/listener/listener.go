// Package listener is the daemon-side inbound half of the Feishu interaction
// subsystem. It consumes card callback events (via notify.Consumer), authorizes
// them against the operator allowlist, and dispatches each to the matching handler:
//
//   - answer  → a needs-info clarification reply; records it and re-triggers the fix
//   - approve → an MR-approval; resumes the verified two-phase execute (opens the MR)
//   - reject  → downgrades a held proposal to auto-diagnose; no MR is ever opened
//   - pause / resume / status / rerun → poller control
//
// It runs as a goroutine inside the poller daemon, sharing the same mutex-guarded
// *state.State and cancellation context, so there is no cross-process state race
// and it stops on the same SIGTERM as the poller. Long work (an agent run) is
// launched in a background goroutine so the event stream is never blocked.
package listener

import (
	"context"
	"log"
	"strings"
	"sync"

	"github.com/fatecannotbealtered/auto-bug-fix/internal/agent"
	"github.com/fatecannotbealtered/auto-bug-fix/internal/notify"
	"github.com/fatecannotbealtered/auto-bug-fix/internal/state"
)

// Retriggerer re-runs a fix for issueKey with humanContext (accumulated needs-info
// answers) injected, recording the run in state exactly as the poller would.
type Retriggerer interface {
	Retrigger(issueKey, humanContext string) (agent.Result, error)
}

// Approver resolves a held MR-approval: Approve resumes Phase B (opens the MR);
// Reject downgrades the proposal to auto-diagnose (no MR is opened).
type Approver interface {
	Approve(issueKey string) (agent.Result, error)
	Reject(issueKey, reason string) error
}

// Controller drives the poller from the control card.
type Controller interface {
	Pause()
	Resume()
	Rerun(issueKey string) (agent.Result, error)
	// SendStatus sends/refreshes a control card with the current poller status to
	// the given operator open_id.
	SendStatus(toOpenID string)
}

// Deps is the listener's collaborators. Retrigger is required (Phase 1); Approver
// and Controller are nil when their features are off.
type Deps struct {
	Consumer          notify.Consumer
	State             *state.State
	StatePath         string
	AuthorizedOpenIDs []string
	Retrigger         Retriggerer
	Approver          Approver
	Controller        Controller
}

// Listener consumes and dispatches inbound card callbacks.
type Listener struct {
	d          Deps
	authorized map[string]bool

	mu       sync.Mutex
	seen     map[string]bool // event_id dedup (current generation)
	seenPrev map[string]bool // previous generation, kept so recent ids still dedup
	inFlight map[string]bool // issue keys with an agent run already in flight
}

// maxSeen bounds the dedup set: when the current generation fills, it rotates to
// seenPrev and a fresh map starts, so memory stays O(maxSeen) on a long-lived
// daemon while recent event_ids still deduplicate.
const maxSeen = 4096

// New builds a Listener. Only callbacks whose server-delivered operator_id is in
// AuthorizedOpenIDs are acted on.
func New(d Deps) *Listener {
	auth := make(map[string]bool, len(d.AuthorizedOpenIDs))
	for _, id := range d.AuthorizedOpenIDs {
		if id = strings.TrimSpace(id); id != "" {
			auth[id] = true
		}
	}
	return &Listener{
		d:          d,
		authorized: auth,
		seen:       map[string]bool{},
		seenPrev:   map[string]bool{},
		inFlight:   map[string]bool{},
	}
}

// Run consumes callbacks until ctx is cancelled.
func (l *Listener) Run(ctx context.Context) error {
	log.Printf("[auto-bug-fix] interact: starting card callback listener (%d authorized operator(s))", len(l.authorized))
	err := l.d.Consumer.Consume(ctx, l.handle)
	log.Printf("[auto-bug-fix] interact: card callback listener stopped")
	return err
}

// handle authorizes and routes one callback. It returns quickly.
func (l *Listener) handle(ev notify.CallbackEvent) error {
	if l.alreadySeen(ev.EventID) {
		return nil
	}
	// Authorize ONLY on the server-delivered operator_id — never on card-carried
	// values, which are attacker-influenceable.
	if !l.authorized[ev.OperatorID] {
		log.Printf("[auto-bug-fix] interact: operator %s is not authorized — ignoring %q", ev.OperatorID, ev.Action)
		return nil
	}
	switch ev.Action {
	case "answer":
		l.handleAnswer(ev)
	case "approve":
		l.handleApprove(ev)
	case "reject":
		l.handleReject(ev)
	case "pause", "resume", "status", "rerun":
		l.handleControl(ev)
	default:
		log.Printf("[auto-bug-fix] interact: unknown action %q — ignored", ev.Action)
	}
	return nil
}

// handleAnswer records a needs-info answer and re-triggers the fix with all
// accumulated answers as context.
func (l *Listener) handleAnswer(ev notify.CallbackEvent) {
	answer := strings.TrimSpace(ev.Answer)
	if ev.Issue == "" || answer == "" {
		return
	}
	l.d.State.AddClarificationAnswer(ev.Issue, answer, ev.OperatorID)
	l.saveState()

	// Atomically claim the issue against BOTH a concurrent poll cycle and another
	// callback. If it is already triggered/awaiting, the answer is recorded above and
	// will be read by the next run.
	if !l.d.State.TryClaimForRun(ev.Issue) {
		log.Printf("[auto-bug-fix] interact: %s already running/awaiting; answer recorded for the next run", ev.Issue)
		return
	}
	humanContext := l.d.State.ClarificationContext(ev.Issue)
	log.Printf("[auto-bug-fix] interact: %s answered %s — re-triggering fix", ev.OperatorID, ev.Issue)
	go func() {
		res, err := l.d.Retrigger.Retrigger(ev.Issue, humanContext)
		if err != nil {
			log.Printf("[auto-bug-fix] interact: re-trigger of %s failed: %v", ev.Issue, err)
			return
		}
		log.Printf("[auto-bug-fix] interact: %s re-run outcome: %s", ev.Issue, res.Outcome)
	}()
}

func (l *Listener) handleApprove(ev notify.CallbackEvent) {
	if l.d.Approver == nil || ev.Issue == "" {
		return
	}
	if !l.claimInFlight(ev.Issue) {
		log.Printf("[auto-bug-fix] interact: %s approval already in progress — ignoring duplicate", ev.Issue)
		return
	}
	log.Printf("[auto-bug-fix] interact: %s approved %s — opening MR", ev.OperatorID, ev.Issue)
	go func() {
		defer l.releaseInFlight(ev.Issue)
		res, err := l.d.Approver.Approve(ev.Issue)
		if err != nil {
			log.Printf("[auto-bug-fix] interact: approve execute of %s failed: %v", ev.Issue, err)
			return
		}
		log.Printf("[auto-bug-fix] interact: %s approved run outcome: %s (mr=%s)", ev.Issue, res.Outcome, res.MRURL)
	}()
}

func (l *Listener) handleReject(ev notify.CallbackEvent) {
	if l.d.Approver == nil || ev.Issue == "" {
		return
	}
	// Serialize reject against an in-flight approve for the same issue: without this
	// a reject could run while an approve goroutine is mid-execute (opening the MR),
	// racing state and opening an MR for a rejected fix. First action in flight wins;
	// if approve is already executing, the reject is ignored (approve completes).
	if !l.claimInFlight(ev.Issue) {
		log.Printf("[auto-bug-fix] interact: %s already has an action in flight — reject ignored", ev.Issue)
		return
	}
	defer l.releaseInFlight(ev.Issue)
	reason := strings.TrimSpace(ev.Fields["reason"])
	log.Printf("[auto-bug-fix] interact: %s rejected %s (reason: %q)", ev.OperatorID, ev.Issue, reason)
	if err := l.d.Approver.Reject(ev.Issue, reason); err != nil {
		log.Printf("[auto-bug-fix] interact: reject of %s failed: %v", ev.Issue, err)
	}
}

func (l *Listener) handleControl(ev notify.CallbackEvent) {
	if l.d.Controller == nil {
		return
	}
	switch ev.Action {
	case "pause":
		l.d.Controller.Pause()
		l.d.Controller.SendStatus(ev.OperatorID)
	case "resume":
		l.d.Controller.Resume()
		l.d.Controller.SendStatus(ev.OperatorID)
	case "status":
		l.d.Controller.SendStatus(ev.OperatorID)
	case "rerun":
		issue := strings.TrimSpace(ev.Fields["issueKey"])
		if issue == "" {
			log.Printf("[auto-bug-fix] interact: rerun with no issue key — ignored")
			return
		}
		if !l.d.State.TryClaimForRun(issue) {
			log.Printf("[auto-bug-fix] interact: %s already running/awaiting — ignoring rerun", issue)
			return
		}
		log.Printf("[auto-bug-fix] interact: %s requested rerun of %s", ev.OperatorID, issue)
		go func() {
			res, err := l.d.Controller.Rerun(issue)
			if err != nil {
				log.Printf("[auto-bug-fix] interact: rerun of %s failed: %v", issue, err)
				return
			}
			log.Printf("[auto-bug-fix] interact: %s rerun outcome: %s", issue, res.Outcome)
		}()
	}
}

func (l *Listener) saveState() {
	if err := l.d.State.Save(l.d.StatePath); err != nil {
		log.Printf("[auto-bug-fix] interact: state save error: %v", err)
	}
}

func (l *Listener) alreadySeen(eventID string) bool {
	if eventID == "" {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.seen[eventID] || l.seenPrev[eventID] {
		return true
	}
	if len(l.seen) >= maxSeen {
		l.seenPrev = l.seen
		l.seen = make(map[string]bool, maxSeen)
	}
	l.seen[eventID] = true
	return false
}

func (l *Listener) claimInFlight(issueKey string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inFlight[issueKey] {
		return false
	}
	l.inFlight[issueKey] = true
	return true
}

func (l *Listener) releaseInFlight(issueKey string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.inFlight, issueKey)
}
