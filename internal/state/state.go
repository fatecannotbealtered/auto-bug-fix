package state

import (
	"encoding/json"
	"errors"
	"os"
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
}

type State struct {
	mu         sync.RWMutex
	LastPollAt time.Time              `json:"lastPollAt"`
	Issues     map[string]*IssueEntry `json:"issues"`
}

func New() *State {
	return &State{Issues: make(map[string]*IssueEntry)}
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
	return s, nil
}

// Save writes state to path atomically (write to temp then rename).
func (s *State) Save(path string) error {
	s.mu.RLock()
	data, err := json.MarshalIndent(s, "", "  ")
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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
	entry, ok := s.Issues[issueKey]
	if !ok {
		return true
	}
	switch entry.Status {
	case StatusInterrupted:
		return true
	case StatusTriggered:
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
