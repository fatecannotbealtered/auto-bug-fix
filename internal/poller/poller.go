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

type CommandReporter interface {
	Command() string
}

// PollOnce runs a single poll cycle: list issues, skip known, trigger new ones concurrently.
func PollOnce(jira JiraLister, trigger Triggerer, st *state.State, filter config.FilterConfig, maxConcurrent int, stateExpiryDays int) error {
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
		if !st.ShouldTrigger(key, stateExpiryDays) {
			continue
		}
		st.StartIssue(key, agentCommand)
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
			}
			if err != nil {
				details.LastError = err.Error()
				st.FinishIssue(key, state.StatusFailed, details)
				log.Printf("[auto-bug-fix] agent error for %s: %v", key, err)
			} else {
				st.FinishIssue(key, statusForOutcome(result.Outcome), details)
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

// Run polls on the given interval until ctx is cancelled.
func Run(ctx context.Context, jira JiraLister, trigger Triggerer, st *state.State, filter config.FilterConfig, interval time.Duration, statePath string, maxConcurrent int, stateExpiryDays int) {
	tick := time.NewTicker(interval)
	defer tick.Stop()

	poll := func() {
		if err := PollOnce(jira, trigger, st, filter, maxConcurrent, stateExpiryDays); err != nil {
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

// parseJiraKeys extracts issue keys from jira-cli search JSON output.
func parseJiraKeys(data []byte) ([]string, error) {
	var result searchResult
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("parse jira-cli output: %w", err)
	}
	keys := make([]string, 0, len(result.Issues))
	for _, issue := range result.Issues {
		if issue.Key != "" {
			keys = append(keys, issue.Key)
		}
	}
	return keys, nil
}

// ParseJiraKeysForTest exposes parseJiraKeys for unit testing.
func ParseJiraKeysForTest(data []byte) ([]string, error) { return parseJiraKeys(data) }
