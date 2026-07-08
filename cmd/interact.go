package cmd

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/fatecannotbealtered/auto-bug-fix/internal/agent"
	"github.com/fatecannotbealtered/auto-bug-fix/internal/config"
	"github.com/fatecannotbealtered/auto-bug-fix/internal/git"
	"github.com/fatecannotbealtered/auto-bug-fix/internal/notify"
	"github.com/fatecannotbealtered/auto-bug-fix/internal/poller"
	"github.com/fatecannotbealtered/auto-bug-fix/internal/state"
)

// interactHub is the daemon-side handler for all Feishu interactions. One value
// implements every role the listener and poller need:
//   - listener.Retriggerer  (needs-info answer → re-run)
//   - listener.Approver     (approve/reject a held MR)
//   - listener.Controller   (pause/resume/rerun/status)
//   - poller.ApprovalRecorder (persist + card a fix awaiting approval)
//
// It records every run in state exactly as the poll loop does, and re-uses the same
// StatusForOutcome mapping and fallback-card behavior so an interaction-driven run
// is indistinguishable from a polled one.
type interactHub struct {
	trigger   *agentTrigger
	st        *state.State
	statePath string
	cfg       config.Config
	paused    atomic.Bool
}

func newInteractHub(trigger *agentTrigger, st *state.State, statePath string, cfg config.Config) *interactHub {
	return &interactHub{trigger: trigger, st: st, statePath: statePath, cfg: cfg}
}

// ── run + finish (shared by re-trigger and rerun) ────────────────────────────

func (h *interactHub) runFix(issueKey, humanContext string) (agent.Result, error) {
	// A re-trigger supersedes any prior awaiting-approval for this issue: clear the
	// pending record so the timeout sweeper can't later fire a spurious downgrade on
	// an orphan. If the re-run itself resolves to awaiting-approval, finishRun's
	// RecordAwaitingApproval writes a fresh record.
	h.st.DeletePendingApproval(issueKey)
	h.st.StartIssue(issueKey, h.trigger.Command())
	h.save()
	result, err := h.trigger.TriggerWithContext(issueKey, humanContext)
	h.finishRun(issueKey, result, err)
	return result, err
}

// finishRun records a completed run: failed / awaiting-approval / done|waiting,
// mirroring poller.PollOnce so state is consistent across trigger paths.
func (h *interactHub) finishRun(issueKey string, result agent.Result, err error) {
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
		h.st.FinishIssue(issueKey, state.StatusFailed, details)
	case result.AwaitingApproval:
		h.st.FinishIssue(issueKey, state.StatusAwaitingApproval, details)
		h.RecordAwaitingApproval(issueKey, result)
	default:
		h.st.FinishIssue(issueKey, poller.StatusForOutcome(result.Outcome), details)
		// Notify the operator when the run produced no marker, OR when the guard
		// downgraded a proposal (Verdict==refute) — e.g. an approve whose integrity
		// re-check failed, where Phase B never ran and the agent sent no card, so the
		// operator would otherwise get zero feedback that their approve was downgraded.
		if h.cfg.Notify.Enabled && (result.NoMarker || result.Verdict == agent.VerdictRefute) {
			if nerr := sendFallbackCard(h.cfg, issueKey, cliRunner); nerr != nil {
				log.Printf("[auto-bug-fix] interact: fallback notification not delivered for %s: %v", issueKey, nerr)
			}
		}
	}
	h.save()
}

// ── listener.Retriggerer / listener.Controller.Rerun ─────────────────────────

func (h *interactHub) Retrigger(issueKey, humanContext string) (agent.Result, error) {
	return h.runFix(issueKey, humanContext)
}

func (h *interactHub) Rerun(issueKey string) (agent.Result, error) {
	return h.runFix(issueKey, "")
}

// ── listener.Controller (pause/resume/status) ────────────────────────────────

func (h *interactHub) Pause() {
	h.paused.Store(true)
	log.Printf("[auto-bug-fix] interact: poller paused")
}
func (h *interactHub) Resume() {
	h.paused.Store(false)
	log.Printf("[auto-bug-fix] interact: poller resumed")
}
func (h *interactHub) Paused() bool { return h.paused.Load() }

func (h *interactHub) SendStatus(toOpenID string) {
	ic, ok := h.interactiveChannel()
	if !ok {
		return
	}
	card, err := ic.RenderControl(h.statusSummary())
	if err != nil {
		log.Printf("[auto-bug-fix] interact: render control card: %v", err)
		return
	}
	to := strings.TrimSpace(toOpenID)
	if to == "" {
		to = h.recipient()
	}
	if to == "" {
		return
	}
	if _, err := ic.Send(to, card, cliRunner); err != nil {
		log.Printf("[auto-bug-fix] interact: control card not delivered to %s: %v", to, err)
	}
}

func (h *interactHub) statusSummary() string {
	c := h.st.StatusCounts()
	run := "运行中"
	if h.Paused() {
		run = "已暂停"
	}
	return fmt.Sprintf("轮询：**%s**\n进行中 %d · 待审批 %d · 待补充/诊断 %d · 已完成 %d · 失败 %d",
		run, c[state.StatusTriggered], c[state.StatusAwaitingApproval], c[state.StatusWaiting], c[state.StatusDone], c[state.StatusFailed])
}

// ── poller.ApprovalRecorder ──────────────────────────────────────────────────

func (h *interactHub) RecordAwaitingApproval(issueKey string, result agent.Result) {
	h.st.SetPendingApproval(state.PendingApproval{
		IssueKey:   issueKey,
		Workspace:  result.Workspace,
		FixBranch:  result.Branch,
		BaseBranch: result.Base,
		HeadSHA:    result.Head,
		Evidence:   result.Evidence,
	})
	h.save()
	h.sendApprovalCard(issueKey, result)
}

func (h *interactHub) sendApprovalCard(issueKey string, result agent.Result) {
	ic, ok := h.interactiveChannel()
	if !ok {
		return
	}
	params := notify.Params{
		Issue:   issueKey,
		Outcome: agent.OutcomeAutoFix,
		Branch:  result.Branch,
		Service: filepath.Base(result.Workspace),
	}
	card, err := ic.RenderApproval(params, approvalDiffSummary(result), notify.Correlation{Action: "approve", Issue: issueKey})
	if err != nil {
		log.Printf("[auto-bug-fix] interact: render approval card for %s: %v", issueKey, err)
		return
	}
	to := h.recipient()
	if to == "" {
		log.Printf("[auto-bug-fix] interact: WARNING no recipient for %s approval card (set notify.target or interact.authorizedOpenIds)", issueKey)
		return
	}
	if _, err := ic.Send(to, card, cliRunner); err != nil {
		log.Printf("[auto-bug-fix] interact: approval card not delivered for %s: %v", issueKey, err)
	}
}

// ── listener.Approver ────────────────────────────────────────────────────────

func (h *interactHub) Approve(issueKey string) (agent.Result, error) {
	p, ok := h.st.GetPendingApproval(issueKey)
	if !ok {
		return agent.Result{}, fmt.Errorf("no pending approval for %s", issueKey)
	}
	proposal := agent.Result{
		MarkerKind: agent.MarkerProposal,
		Outcome:    agent.OutcomeAutoFix,
		Workspace:  p.Workspace,
		Branch:     p.FixBranch,
		Base:       p.BaseBranch,
		Head:       p.HeadSHA,
		Evidence:   p.Evidence,
	}
	h.st.StartIssue(issueKey, h.trigger.Command())
	h.save()
	result, err := h.trigger.ExecuteApproved(issueKey, proposal)
	// Consume the pending approval only when execute RESOLVED it — an MR opened, or
	// an integrity re-check downgrade (err==nil). On a transient error keep it so a
	// re-click can retry the idempotent guard.Execute (which re-checks HeadSHA)
	// rather than discarding the verified, human-approved proposal.
	if err == nil {
		h.st.DeletePendingApproval(issueKey)
	}
	h.finishRun(issueKey, result, err)
	return result, err
}

func (h *interactHub) Reject(issueKey, reason string) error {
	if _, ok := h.st.GetPendingApproval(issueKey); !ok {
		return fmt.Errorf("no pending approval for %s", issueKey)
	}
	msg := "human rejected"
	if r := strings.TrimSpace(reason); r != "" {
		msg += ": " + r
	}
	// No MR was ever opened, so nothing to revoke — record a human-owned downgrade.
	h.st.FinishIssue(issueKey, state.StatusWaiting, state.FinishDetails{
		Outcome:      agent.OutcomeAutoDiagnose,
		Verdict:      agent.VerdictRefute,
		VerifyReason: msg,
	})
	h.st.DeletePendingApproval(issueKey)
	h.save()
	return nil
}

// ── timeout sweep ────────────────────────────────────────────────────────────

// RunSweeper periodically gives up on unanswered approvals (which the poller never
// re-triggers) and prunes stale clarification records, bounded by
// interact.timeoutHours. It stops on ctx cancellation.
func (h *interactHub) RunSweeper(ctx context.Context) {
	if h.cfg.Interact.TimeoutHours <= 0 {
		return
	}
	interval := time.Duration(h.cfg.Interact.TimeoutHours) * time.Hour / 6
	if interval > time.Hour {
		interval = time.Hour
	}
	if interval < time.Minute {
		interval = time.Minute
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.sweepExpired()
		}
	}
}

// sweepExpired downgrades approvals older than the timeout and prunes old
// clarifications. An expired approval becomes a human-owned auto-diagnose (no MR
// was ever opened), so a stranded fix cannot sit awaiting-approval forever.
func (h *interactHub) sweepExpired() {
	if h.cfg.Interact.TimeoutHours <= 0 {
		return
	}
	cutoff := time.Now().Add(-time.Duration(h.cfg.Interact.TimeoutHours) * time.Hour)
	changed := false
	for _, key := range h.st.ExpiredApprovalKeys(cutoff) {
		h.st.FinishIssue(key, state.StatusWaiting, state.FinishDetails{
			Outcome:      agent.OutcomeAutoDiagnose,
			Verdict:      agent.VerdictRefute,
			VerifyReason: fmt.Sprintf("approval timed out after %dh with no human decision", h.cfg.Interact.TimeoutHours),
		})
		h.st.DeletePendingApproval(key)
		changed = true
		log.Printf("[auto-bug-fix] interact: approval for %s timed out after %dh — downgraded to auto-diagnose", key, h.cfg.Interact.TimeoutHours)
	}
	if n := h.st.PruneClarifications(cutoff); n > 0 {
		changed = true
		log.Printf("[auto-bug-fix] interact: pruned %d stale clarification record(s)", n)
	}
	if changed {
		h.save()
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func (h *interactHub) save() {
	if err := h.st.Save(h.statePath); err != nil {
		log.Printf("[auto-bug-fix] interact: state save error: %v", err)
	}
}

func (h *interactHub) interactiveChannel() (notify.InteractiveChannel, bool) {
	if !h.cfg.Notify.Enabled {
		return nil, false
	}
	ch, err := notify.Get(h.cfg.Notify.Channel)
	if err != nil {
		return nil, false
	}
	ic, ok := ch.(notify.InteractiveChannel)
	return ic, ok
}

// recipient is the fallback destination for daemon-sent cards (approval/control):
// the configured notify.target (may be a group) or the first authorized operator.
func (h *interactHub) recipient() string {
	if t := strings.TrimSpace(h.cfg.Notify.Target); t != "" {
		return t
	}
	for _, id := range h.cfg.Interact.AuthorizedOpenIDs {
		if id = strings.TrimSpace(id); id != "" {
			return id
		}
	}
	return ""
}

// approvalDiffSummary builds a short, human-readable change summary for the
// approval card from the real committed diff (objective; not agent-reported).
func approvalDiffSummary(r agent.Result) string {
	if r.Workspace == "" || r.Base == "" || r.Head == "" {
		return ""
	}
	var head string
	if stat, err := git.Numstat(r.Workspace, r.Base, r.Head); err == nil {
		head = fmt.Sprintf("%d 文件，+%d −%d", stat.Files, stat.Added, stat.Deleted)
	}
	diff, err := git.DiffText(r.Workspace, r.Base, r.Head)
	if err != nil {
		return head
	}
	body := truncateRunes(sanitizeFence(diff), 1500)
	return strings.TrimSpace(head + "\n\n" + body)
}

// sanitizeFence keeps an embedded ``` in a diff from breaking the card's markdown
// code fence.
func sanitizeFence(s string) string { return strings.ReplaceAll(s, "```", "` ` `") }

// truncateRunes cuts s to at most maxRunes runes (never mid-rune) with an ellipsis.
func truncateRunes(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes]) + "\n…（diff 已截断，见分支）"
}
