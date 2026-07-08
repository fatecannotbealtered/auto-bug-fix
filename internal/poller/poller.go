package poller

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/fatecannotbealtered/auto-bug-fix/internal/agent"
	"github.com/fatecannotbealtered/auto-bug-fix/internal/config"
	"github.com/fatecannotbealtered/auto-bug-fix/internal/state"
)

// JiraLister lists Bug issue keys matching the given filter.
type JiraLister interface {
	ListIssues(filter config.FilterConfig) ([]string, error)
}

// Triggerer spawns the agent for an issue key.
type Triggerer interface {
	Trigger(issueKey string) (agent.Result, error)
}

// Notifier sends a completion notification for an issue. The poller calls it only
// when the agent finished without a marker (so it almost certainly didn't run its
// own notify step); a nil Notifier disables the fallback.
type Notifier interface {
	Notify(issueKey string, result agent.Result)
}

type CommandReporter interface {
	Command() string
}

// ApprovalRecorder is called when a fix passed the AI verifier but a human
// MR-approval is required (result.AwaitingApproval): it persists the proposal and
// sends the approve/reject card. nil disables the approval gate at the poll layer.
type ApprovalRecorder interface {
	RecordAwaitingApproval(issueKey string, result agent.Result)
}

// Hooks bundles the poll loop's optional collaborators so the core signature stays
// small as features are added. All fields are nil-safe.
type Hooks struct {
	// Notifier sends a fallback completion card when the agent printed no marker.
	Notifier Notifier
	// ApprovalRecorder records + cards a fix awaiting human MR-approval.
	ApprovalRecorder ApprovalRecorder
	// Paused, when set and returning true, makes Run skip a poll cycle (used by the
	// Feishu control card's pause/resume). Consulted by Run, not PollOnce.
	Paused func() bool
}

// PollOnce runs a single poll cycle: list issues, skip known, trigger new ones concurrently.
func PollOnce(jira JiraLister, trigger Triggerer, st *state.State, filter config.FilterConfig, maxConcurrent int, stateExpiryDays int, hooks Hooks) error {
	keys, err := jira.ListIssues(filter)
	if err != nil {
		return fmt.Errorf("jira list: %w", err)
	}

	var toTrigger []string
	agentCommand := ""
	if reporter, ok := trigger.(CommandReporter); ok {
		agentCommand = reporter.Command()
	}
	for _, key := range keys {
		// Atomic claim (check + mark triggered under one lock) so the poll loop and a
		// concurrent listener re-trigger can never both start the same issue.
		if !st.ClaimForPoll(key, stateExpiryDays, agentCommand) {
			continue
		}
		toTrigger = append(toTrigger, key)
	}

	if len(toTrigger) == 0 {
		return nil
	}

	if maxConcurrent <= 0 {
		maxConcurrent = 3
	}

	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup
	for _, key := range toTrigger {
		key := key
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			result, err := trigger.Trigger(key)
			details := state.FinishDetails{
				Outcome:        result.Outcome,
				MRURL:          result.MRURL,
				HandoffPath:    result.HandoffPath,
				ExitCode:       result.ExitCode,
				DurationMillis: result.DurationMillis,
				Verdict:        result.Verdict,
				VerifyReason:   result.VerifyReason,
			}
			switch {
			case err != nil:
				details.LastError = err.Error()
				st.FinishIssue(key, state.StatusFailed, details)
				log.Printf("[auto-bug-fix] agent error for %s: %v", key, err)
			case result.AwaitingApproval:
				// AI upheld, but a human MR-approval is required: hold the fix and
				// let the recorder persist the proposal + send the approve/reject card.
				st.FinishIssue(key, state.StatusAwaitingApproval, details)
				if hooks.ApprovalRecorder != nil {
					hooks.ApprovalRecorder.RecordAwaitingApproval(key, result)
				}
			default:
				st.FinishIssue(key, statusForOutcome(result.Outcome), details)
				// Send a fallback card when the agent ran its own notify step but the
				// harness overrode the result: no marker (agent didn't notify), OR the
				// guard downgraded a proposal (Verdict==refute) so Phase B never ran and
				// no agent card was sent — either way the operator would get zero feedback.
				if (result.NoMarker || result.Verdict == agent.VerdictRefute) && hooks.Notifier != nil {
					hooks.Notifier.Notify(key, result)
				}
			}
		}()
	}
	wg.Wait()
	return nil
}

// statusForOutcome maps a clean agent run to a persisted status. needs-info and
// auto-diagnose mean the agent ran but did NOT fix the bug — a human now owns the
// ticket — so they are recorded as "waiting", not "done". Recording them as done
// would, under the default stateExpiryDays=0, make still-open work permanently
// terminal. auto-fix and a missing marker fall through to "done".
func statusForOutcome(outcome string) state.Status {
	switch outcome {
	case agent.OutcomeNeedsInfo, agent.OutcomeAutoDiagnose:
		return state.StatusWaiting
	default:
		return state.StatusDone
	}
}

// StatusForOutcome exposes the same outcome→status mapping the poll loop uses, so
// the interaction listener's re-trigger path records a finished run identically.
func StatusForOutcome(outcome string) state.Status { return statusForOutcome(outcome) }

// Run polls on the given interval until ctx is cancelled.
func Run(ctx context.Context, jira JiraLister, trigger Triggerer, st *state.State, filter config.FilterConfig, interval time.Duration, statePath string, maxConcurrent int, stateExpiryDays int, hooks Hooks) {
	tick := time.NewTicker(interval)
	defer tick.Stop()

	poll := func() {
		if hooks.Paused != nil && hooks.Paused() {
			log.Printf("[auto-bug-fix] poll paused; skipping this cycle")
			return
		}
		if err := PollOnce(jira, trigger, st, filter, maxConcurrent, stateExpiryDays, hooks); err != nil {
			log.Printf("[auto-bug-fix] poll error: %v", err)
			return
		}
		if statePath != "" {
			if err := st.Save(statePath); err != nil {
				log.Printf("[auto-bug-fix] state save error: %v", err)
			}
		}
	}

	poll() // run immediately on start
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			poll()
		}
	}
}

// CLIJira implements JiraLister using jira-cli search with --all pagination.
type CLIJira struct{}

func (c *CLIJira) ListIssues(filter config.FilterConfig) ([]string, error) {
	args := buildArgs(filter)
	out, err := exec.Command("jira-cli", args...).Output() //nolint:gosec
	if err != nil {
		return nil, fmt.Errorf("jira-cli: %w", err)
	}
	return parseJiraKeys(out)
}

// buildArgs translates FilterConfig into jira-cli search flags.
func buildArgs(filter config.FilterConfig) []string {
	var clauses []string
	clauses = append(clauses, "issuetype = Bug")
	clauses = append(clauses, "statusCategory != Done")

	if filter.TitleContains != "" {
		clauses = append(clauses, fmt.Sprintf(`summary ~ %q`, filter.TitleContains))
	}
	if filter.AssignedToMe {
		clauses = append(clauses, "assignee = currentUser()")
	}
	for _, s := range filter.ExcludeStatuses {
		clauses = append(clauses, fmt.Sprintf(`status != %q`, s))
	}

	jql := strings.Join(clauses, " AND ")
	return []string{"search", jql, "--all", "--json", "--quiet"}
}

// searchResult matches jira-cli search JSON output.
type searchResult struct {
	Issues []struct {
		Key string `json:"key"`
	} `json:"issues"`
}

type searchEnvelope struct {
	OK    *bool        `json:"ok"`
	Data  searchResult `json:"data"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// parseJiraKeys extracts issue keys from jira-cli search JSON output.
func parseJiraKeys(data []byte) ([]string, error) {
	var env searchEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("parse jira-cli output: %w", err)
	}
	if env.OK != nil {
		if !*env.OK {
			msg := "jira-cli returned ok:false"
			if env.Error != nil && env.Error.Message != "" {
				msg = env.Error.Message
			}
			if env.Error != nil && env.Error.Code != "" {
				msg = env.Error.Code + ": " + msg
			}
			return nil, fmt.Errorf("%s", msg)
		}
		return keysFromSearchResult(env.Data), nil
	}

	var result searchResult
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("parse jira-cli output: %w", err)
	}
	return keysFromSearchResult(result), nil
}

func keysFromSearchResult(result searchResult) []string {
	keys := make([]string, 0, len(result.Issues))
	for _, issue := range result.Issues {
		if issue.Key != "" {
			keys = append(keys, issue.Key)
		}
	}
	return keys
}

// ParseJiraKeysForTest exposes parseJiraKeys for unit testing.
func ParseJiraKeysForTest(data []byte) ([]string, error) { return parseJiraKeys(data) }
