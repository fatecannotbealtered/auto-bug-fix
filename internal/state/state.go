package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Status string

const (
	StatusTriggered   Status = "triggered"
	StatusDone        Status = "done"
	StatusFailed      Status = "failed"
	StatusInterrupted Status = "interrupted"
	// StatusWaiting means the agent ran cleanly but did not fix the bug
	// (outcome needs-info or auto-diagnose) — a human now owns the ticket.
	// Distinct from done so the issue is not mislabeled as completed.
	StatusWaiting Status = "waiting"
	// StatusAwaitingApproval means an auto-fix proposal passed the AI verifier and
	// is held pending a human approve/reject on a Feishu card. The poller must not
	// re-trigger it (a human decision is pending); the listener resumes or rejects.
	StatusAwaitingApproval Status = "awaiting-approval"
)

type IssueEntry struct {
	Status         Status    `json:"status"`
	TriggeredAt    time.Time `json:"triggeredAt"`
	StartedAt      time.Time `json:"startedAt,omitempty"`
	CompletedAt    time.Time `json:"completedAt,omitempty"`
	UpdatedAt      time.Time `json:"updatedAt,omitempty"`
	AgentCommand   string    `json:"agentCommand,omitempty"`
	Attempts       int       `json:"attempts,omitempty"`
	Outcome        string    `json:"outcome,omitempty"`
	MRURL          string    `json:"mrUrl,omitempty"`
	HandoffPath    string    `json:"handoffPath,omitempty"`
	ExitCode       int       `json:"exitCode,omitempty"`
	DurationMillis int64     `json:"durationMillis,omitempty"`
	LastError      string    `json:"lastError,omitempty"`
	// Verdict records the guard's decision when the two-phase evidence gate is on:
	// "refute" means an auto-fix proposal was downgraded to auto-diagnose (by the
	// verifier or an integrity check); empty means the gate did not act.
	// VerifyReason carries the one-line reason for an audit trail.
	Verdict      string `json:"verdict,omitempty"`
	VerifyReason string `json:"verifyReason,omitempty"`
}

// QARound is one exchange in the needs-info clarification loop for an issue: a
// human's answer submitted from an interactive Feishu card. Rounds are durable so
// a re-triggered fix accumulates every prior answer as context. The agent's own
// questions are not stored here — it re-derives them from the ticket on re-run.
type QARound struct {
	Answer     string    `json:"answer"`
	AnsweredBy string    `json:"answeredBy,omitempty"` // Lark operator open_id
	AnsweredAt time.Time `json:"answeredAt"`
}

// Clarification tracks the open needs-info clarification loop for one issue. It is
// written only by the daemon (poller + listener share one *State), so there is no
// cross-process race, and it survives daemon restarts.
type Clarification struct {
	IssueKey  string    `json:"issueKey"`
	Rounds    []QARound `json:"rounds,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt,omitempty"`
}

// PendingApproval is a fix that passed the AI verifier and awaits a human
// approve/reject. It captures everything needed to resume the two-phase execute
// (push + MR + Jira) later, so the gate survives a daemon restart: the local
// commit is durable on disk and HeadSHA is re-checked before executing.
type PendingApproval struct {
	IssueKey   string    `json:"issueKey"`
	Workspace  string    `json:"workspace"`
	FixBranch  string    `json:"fixBranch"`
	BaseBranch string    `json:"baseBranch"`
	HeadSHA    string    `json:"headSha"`
	Evidence   string    `json:"evidence,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
}

type State struct {
	mu         sync.RWMutex
	saveMu     sync.Mutex             // serializes Save (marshal→write→rename) across concurrent savers
	LastPollAt time.Time              `json:"lastPollAt"`
	Issues     map[string]*IssueEntry `json:"issues"`
	// Clarifications holds the durable needs-info clarification loop per issue key.
	Clarifications map[string]*Clarification `json:"clarifications,omitempty"`
	// Approvals holds fixes awaiting a human MR-approval, keyed by issue key.
	Approvals map[string]*PendingApproval `json:"approvals,omitempty"`
}

func New() *State {
	return &State{
		Issues:         make(map[string]*IssueEntry),
		Clarifications: make(map[string]*Clarification),
		Approvals:      make(map[string]*PendingApproval),
	}
}

// Load reads state from path. Returns empty state if file does not exist.
func Load(path string) (*State, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return New(), nil
	}
	if err != nil {
		return nil, err
	}
	s := New()
	if err := json.Unmarshal(data, s); err != nil {
		return nil, err
	}
	if s.Issues == nil {
		s.Issues = make(map[string]*IssueEntry)
	}
	if s.Clarifications == nil {
		s.Clarifications = make(map[string]*Clarification)
	}
	if s.Approvals == nil {
		s.Approvals = make(map[string]*PendingApproval)
	}
	return s, nil
}

// Save writes state to path atomically (write to a unique temp then rename).
//
// The whole marshal→write→rename sequence is serialized by saveMu because the
// daemon now saves from several goroutines concurrently (the poll loop, per-issue
// poll goroutines recording an awaiting-approval, and the interaction listener).
// Without this, two savers could race on a shared temp file, or an earlier
// snapshot could rename over a newer one (lost update). saveMu guarantees the
// on-disk file always ends at the most-recently-marshaled state. The field RWMutex
// still guards state access and is held only briefly for the marshal.
func (s *State) Save(path string) error {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()

	s.mu.RLock()
	data, err := json.MarshalIndent(s, "", "  ")
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, werr := f.Write(data); werr != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return werr
	}
	if cerr := f.Close(); cerr != nil {
		_ = os.Remove(tmp)
		return cerr
	}
	if cherr := os.Chmod(tmp, 0o600); cherr != nil {
		_ = os.Remove(tmp)
		return cherr
	}
	if rerr := os.Rename(tmp, path); rerr != nil {
		_ = os.Remove(tmp)
		return rerr
	}
	return nil
}

func (s *State) SetIssue(issueKey string, status Status) {
	if status == StatusTriggered {
		s.StartIssue(issueKey, "")
		return
	}
	s.FinishIssue(issueKey, status, FinishDetails{})
}

func (s *State) StartIssue(issueKey, agentCommand string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.startIssueLocked(issueKey, agentCommand)
}

func (s *State) startIssueLocked(issueKey, agentCommand string) {
	now := time.Now()
	entry, ok := s.Issues[issueKey]
	if !ok {
		entry = &IssueEntry{TriggeredAt: now}
		s.Issues[issueKey] = entry
	}
	if entry.TriggeredAt.IsZero() {
		entry.TriggeredAt = now
	}
	entry.Status = StatusTriggered
	entry.StartedAt = now
	entry.CompletedAt = time.Time{}
	entry.UpdatedAt = now
	entry.AgentCommand = RedactCommand(agentCommand)
	entry.Attempts++
	entry.Outcome = ""
	entry.MRURL = ""
	entry.HandoffPath = ""
	entry.ExitCode = 0
	entry.DurationMillis = 0
	entry.LastError = ""
	entry.Verdict = ""
	entry.VerifyReason = ""
}

// RedactCommand removes obvious inline secrets before a command is written to
// state or returned through context. Secrets should live in each sibling CLI's
// credential store, but this avoids preserving accidental token args.
func RedactCommand(command string) string {
	fields := strings.Fields(command)
	for i := 0; i < len(fields); i++ {
		lower := strings.ToLower(fields[i])
		if containsSecretName(lower) {
			if strings.Contains(fields[i], "=") {
				parts := strings.SplitN(fields[i], "=", 2)
				fields[i] = parts[0] + "=<redacted>"
				continue
			}
			if i+1 < len(fields) {
				fields[i+1] = "<redacted>"
			}
		}
	}
	return strings.Join(fields, " ")
}

func containsSecretName(value string) bool {
	for _, marker := range []string{"token", "password", "passwd", "secret", "authorization", "api-key", "apikey"} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

type FinishDetails struct {
	Outcome        string
	MRURL          string
	HandoffPath    string
	ExitCode       int
	DurationMillis int64
	LastError      string
	Verdict        string
	VerifyReason   string
}

func (s *State) FinishIssue(issueKey string, status Status, details FinishDetails) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	entry, ok := s.Issues[issueKey]
	if !ok {
		entry = &IssueEntry{TriggeredAt: now}
		s.Issues[issueKey] = entry
	}
	entry.Status = status
	entry.CompletedAt = now
	entry.UpdatedAt = now
	entry.Outcome = details.Outcome
	entry.MRURL = details.MRURL
	entry.HandoffPath = details.HandoffPath
	entry.ExitCode = details.ExitCode
	entry.DurationMillis = details.DurationMillis
	entry.LastError = details.LastError
	entry.Verdict = details.Verdict
	entry.VerifyReason = details.VerifyReason
}

// AddClarificationAnswer records one human answer for issueKey's clarification
// loop, lazily creating the record on the first answer. answeredBy is the Lark
// operator open_id (already authorization-checked by the caller).
func (s *State) AddClarificationAnswer(issueKey, answer, answeredBy string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	c, ok := s.Clarifications[issueKey]
	if !ok {
		c = &Clarification{IssueKey: issueKey, CreatedAt: now}
		s.Clarifications[issueKey] = c
	}
	c.Rounds = append(c.Rounds, QARound{Answer: answer, AnsweredBy: answeredBy, AnsweredAt: now})
	c.UpdatedAt = now
}

// ClarificationContext renders every recorded answer for issueKey as a plain text
// block to inject into a re-triggered fix run, or "" when there is none. The block
// is treated as untrusted human input by the agent, not as instructions.
func (s *State) ClarificationContext(issueKey string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.Clarifications[issueKey]
	if !ok || len(c.Rounds) == 0 {
		return ""
	}
	var b strings.Builder
	for i, r := range c.Rounds {
		fmt.Fprintf(&b, "补充%d：%s\n", i+1, strings.TrimSpace(r.Answer))
	}
	return strings.TrimSpace(b.String())
}

func (s *State) markTriggeredLocked(issueKey string) {
	now := time.Now()
	e, ok := s.Issues[issueKey]
	if !ok {
		e = &IssueEntry{TriggeredAt: now}
		s.Issues[issueKey] = e
	}
	if e.TriggeredAt.IsZero() {
		e.TriggeredAt = now
	}
	e.Status = StatusTriggered
	e.UpdatedAt = now
}

// ClaimForPoll atomically decides whether the poll loop should trigger issueKey
// and, if so, marks it triggered and records the run — all under ONE lock, so it
// cannot race a listener's TryClaimForRun. Returns true when the caller now owns
// the run. This replaces the previous ShouldTrigger-then-StartIssue two-step, which
// had a TOCTOU window a concurrent listener re-trigger could slip through.
func (s *State) ClaimForPoll(issueKey string, expiryDays int, agentCommand string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.shouldTriggerLocked(issueKey, expiryDays) {
		return false
	}
	s.startIssueLocked(issueKey, agentCommand)
	return true
}

// TryClaimForRun atomically claims issueKey for a listener-initiated run (needs-info
// answer or control rerun): false when it is already triggered or awaiting approval
// (the poller or another handler owns it), else marks it triggered and returns true.
// Paired with ClaimForPoll under the same lock, the poll loop and the listener can
// never both start the same issue.
func (s *State) TryClaimForRun(issueKey string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.Issues[issueKey]; ok && (e.Status == StatusTriggered || e.Status == StatusAwaitingApproval) {
		return false
	}
	s.markTriggeredLocked(issueKey)
	return true
}

// ExpiredApprovalKeys returns issue keys of pending approvals created before
// cutoff (used by the timeout sweep to give up on unanswered approvals). An
// approval whose issue is currently StatusTriggered is skipped: that means an
// approve is executing it right now, so the sweep must not race the execute and
// spuriously downgrade a fix a human just approved.
func (s *State) ExpiredApprovalKeys(cutoff time.Time) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var keys []string
	for key, p := range s.Approvals {
		if !p.CreatedAt.Before(cutoff) {
			continue
		}
		if e, ok := s.Issues[key]; ok && e.Status == StatusTriggered {
			continue
		}
		keys = append(keys, key)
	}
	return keys
}

// PruneClarifications deletes clarification records not updated since cutoff and
// returns the count removed (bounds the map on a long-lived daemon).
func (s *State) PruneClarifications(cutoff time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for key, c := range s.Clarifications {
		last := c.UpdatedAt
		if last.IsZero() {
			last = c.CreatedAt
		}
		if last.Before(cutoff) {
			delete(s.Clarifications, key)
			n++
		}
	}
	return n
}

// SetPendingApproval records (or replaces) a fix awaiting human approval.
func (s *State) SetPendingApproval(p PendingApproval) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now()
	}
	cp := p
	s.Approvals[p.IssueKey] = &cp
}

// GetPendingApproval returns a copy of the pending approval for issueKey, if any.
func (s *State) GetPendingApproval(issueKey string) (PendingApproval, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.Approvals[issueKey]
	if !ok {
		return PendingApproval{}, false
	}
	return *p, true
}

// DeletePendingApproval removes a resolved (approved or rejected) approval.
func (s *State) DeletePendingApproval(issueKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.Approvals, issueKey)
}

// StatusCounts returns the number of issues in each status, for a status summary.
func (s *State) StatusCounts() map[Status]int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	counts := map[Status]int{}
	for _, e := range s.Issues {
		counts[e.Status]++
	}
	return counts
}

// ReclaimStaleTriggered marks every still-"triggered" issue as "interrupted".
// Call this at poller startup when no other poller is running: any issue left in
// "triggered" means a previous run was killed mid-fix. Returns the count reset.
func (s *State) ReclaimStaleTriggered() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	count := 0
	for _, entry := range s.Issues {
		if entry.Status == StatusTriggered {
			entry.Status = StatusInterrupted
			entry.UpdatedAt = now
			count++
		}
	}
	return count
}

// ShouldTrigger reports whether the poller should (re)trigger this issue.
// Unknown and interrupted issues trigger; running ("triggered") issues do not;
// done/failed/waiting issues trigger only once older than expiryDays (0 = never).
// waiting (needs-info / auto-diagnose) follows the same expiry rule as done: the
// poller cannot see a human's reply, so re-running before expiry would just spam
// the ticket. The reliable re-activation path is `auto-bug-fix fix <key>`, which
// bypasses state entirely.
// The whole decision is taken under a single read lock so it observes one
// consistent snapshot of the entry.
func (s *State) ShouldTrigger(issueKey string, expiryDays int) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.shouldTriggerLocked(issueKey, expiryDays)
}

func (s *State) shouldTriggerLocked(issueKey string, expiryDays int) bool {
	entry, ok := s.Issues[issueKey]
	if !ok {
		return true
	}
	switch entry.Status {
	case StatusInterrupted:
		return true
	case StatusTriggered, StatusAwaitingApproval:
		// A pending human approval is not eligible for re-trigger at any age: the
		// local commit is being held for approve/reject, and re-running Phase A
		// would discard it. The listener resolves it, never the poll loop.
		return false
	default: // done / failed / waiting
		if expiryDays <= 0 {
			return false
		}
		completedAt := entry.CompletedAt
		if completedAt.IsZero() {
			completedAt = entry.TriggeredAt
		}
		return time.Since(completedAt) > time.Duration(expiryDays)*24*time.Hour
	}
}
