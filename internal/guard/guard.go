// Package guard is the deterministic orchestration that puts an evidence gate in
// FRONT of the irreversible write (open MR / write Jira). Today the harness records
// the agent's self-reported outcome verbatim with no veto; guard splits an auto-fix
// run into Phase A (investigate + local commit, no writes), an independent read-only
// verifier, and Phase B (execute the already-decided fix). On a refuted or
// integrity-failed proposal the harness downgrades the outcome to auto-diagnose and
// never spawns Phase B — so an agent merely asserting "evidence exists" cannot
// produce an MR.
//
// Honest boundary: except for kiro (which can drop write tools), Phase A cannot
// PHYSICALLY stop a misbehaving agent from pushing on its own; the gate rests on the
// template contract plus integrity checks the harness CAN verify against the real
// git checkout it inspects (HEAD matches, diff is non-empty). The verifier is an LLM
// with NO write credentials, so its worst case is a false downgrade, never a false
// auto-fix.
package guard

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/fatecannotbealtered/auto-bug-fix/internal/agent"
	"github.com/fatecannotbealtered/auto-bug-fix/internal/git"
)

// Phase selects which slice of the per-ticket workflow a spawn runs.
type Phase string

const (
	// PhaseFull is the single-shot legacy flow (investigate + execute, opens MR).
	// Used when the guard is disabled — behavior identical to pre-guard.
	PhaseFull Phase = "full"
	// PhaseInvestigate stops at a local commit and prints a PROPOSAL; no writes.
	PhaseInvestigate Phase = "investigate"
	// PhaseVerify is the read-only adversarial verifier; prints a VERIFY verdict.
	PhaseVerify Phase = "verify"
	// PhaseExecute pushes the already-decided fix and opens the MR; prints RESULT.
	PhaseExecute Phase = "execute"
)

// Config is the guard's slice of the app config (kept local so the package does
// not depend on internal/config, which keeps it trivially testable).
type Config struct {
	// Enabled turns on the two-phase gate. When false, RunGuarded runs the legacy
	// single spawn unchanged.
	Enabled bool
	// WorkspaceRoot is the configured clone root; a proposal's workspace must live
	// under it (an agent cannot point the verifier at an unrelated clean directory).
	WorkspaceRoot string
	// RequireApproval adds a human approve/reject gate AFTER the AI verifier upholds:
	// RunGuarded stops before Phase B and returns a result with AwaitingApproval set;
	// the caller persists the proposal and later calls Execute on an approve.
	RequireApproval bool
}

// SpawnFunc spawns the agent for issueKey in the given phase and returns the parsed
// marker result. The implementation owns command/marker-prefix/env-per-phase wiring
// (cmd.agentTrigger); guard only chooses the phase and passes phase-specific env.
type SpawnFunc func(issueKey string, phase Phase, extraEnv map[string]string) (agent.Result, error)

// Env keys guard injects for Phase B / verifier (read by the agent templates).
const (
	EnvExpectedHead = "AUTO_BUG_FIX_EXPECTED_HEAD"
	EnvWorkspace    = "AUTO_BUG_FIX_WORKSPACE"
	EnvFixBranch    = "AUTO_BUG_FIX_FIX_BRANCH"
	EnvBaseBranch   = "AUTO_BUG_FIX_BASE_BRANCH"
	EnvVerifyDiff   = "AUTO_BUG_FIX_VERIFY_DIFF"
	EnvVerifyEvid   = "AUTO_BUG_FIX_VERIFY_EVIDENCE"
)

// RunGuarded runs one ticket through the gate and returns the final result. It is
// the single entry point shared by the poller and `fix`.
func RunGuarded(issueKey string, cfg Config, spawn SpawnFunc) (agent.Result, error) {
	if !cfg.Enabled {
		return spawn(issueKey, PhaseFull, nil) // legacy single-shot flow
	}

	// Phase A — investigate; agent commits locally and proposes, no writes.
	res, err := spawn(issueKey, PhaseInvestigate, nil)
	if err != nil {
		return res, err
	}

	// Only an auto-fix PROPOSAL carries a pending write to gate. A terminal marker
	// (auto-diagnose / needs-info / none) produced no side effect — pass it through.
	if res.MarkerKind != agent.MarkerProposal || res.Outcome != agent.OutcomeAutoFix {
		return res, nil
	}

	// Integrity-check the self-reported proposal against the real checkout.
	if reason := checkIntegrity(cfg, res); reason != "" {
		return downgrade(res, "integrity check failed: "+reason), nil
	}

	// Read-only verifier on the real diff.
	verdict, vErr := runVerifier(issueKey, res, spawn)
	if vErr != nil {
		return downgrade(res, "verifier could not run: "+vErr.Error()), nil
	}
	if verdict.Verdict != agent.VerdictUphold {
		return downgrade(res, verdictReason(verdict)), nil
	}

	// AI upheld. If a human approval gate is on, hold here — do NOT run Phase B.
	// The caller persists the proposal (res carries workspace/branch/base/head) and
	// resumes via Execute on an authorized approve. No MR exists yet, so a later
	// reject has nothing to revoke.
	if cfg.RequireApproval {
		res.AwaitingApproval = true
		return res, nil
	}

	// Phase B — execute the already-decided fix (push + MR + Jira).
	return Execute(issueKey, cfg, res, spawn)
}

// Execute runs Phase B (push + MR + Jira) for an already-verified proposal. It is
// the resume point for the human-approval gate: the caller reconstructs proposal
// from the persisted PendingApproval and calls this on an approve. It re-checks
// integrity first, because the local commit has been sitting on disk and could have
// been moved/reset since it was verified.
func Execute(issueKey string, cfg Config, proposal agent.Result, spawn SpawnFunc) (agent.Result, error) {
	if reason := checkIntegrity(cfg, proposal); reason != "" {
		return downgrade(proposal, "integrity re-check failed before execute: "+reason), nil
	}
	return spawn(issueKey, PhaseExecute, map[string]string{
		EnvExpectedHead: proposal.Head,
		EnvWorkspace:    proposal.Workspace,
		EnvFixBranch:    proposal.Branch,
		EnvBaseBranch:   proposal.Base,
	})
}

// checkIntegrity returns "" when the proposal is consistent with the real checkout,
// or a short reason to downgrade. It catches low-effort fabrication (workspace
// outside root, HEAD mismatch, empty diff); it cannot prove the fix is correct.
func checkIntegrity(cfg Config, res agent.Result) string {
	if res.Workspace == "" || res.Head == "" || res.Base == "" || res.Branch == "" {
		return "proposal missing workspace, base, head, or branch"
	}
	if cfg.WorkspaceRoot != "" && !withinRoot(cfg.WorkspaceRoot, res.Workspace) {
		return "workspace is outside the configured workspace root"
	}
	if !strings.HasPrefix(res.Branch, "fix/") {
		return "branch is not a fix/* branch"
	}
	// The evidence path is agent-self-reported and handed to the verifier verbatim;
	// require it to live inside the workspace so it cannot point the verifier at an
	// unrelated or traversal path. withinRoot resolves symlinks, so a forged or
	// dangling path also fails here.
	if res.Evidence != "" && !withinRoot(res.Workspace, res.Evidence) {
		return "evidence path is outside the workspace"
	}
	head, err := git.HeadSHA(res.Workspace)
	if err != nil {
		return "cannot read workspace HEAD"
	}
	if head != res.Head {
		return "workspace HEAD does not match the reported commit"
	}
	stat, err := git.Numstat(res.Workspace, res.Base, res.Head)
	if err != nil {
		return "cannot compute diff base...head"
	}
	if stat.Files == 0 {
		return "diff against base is empty (no committed change)"
	}
	return ""
}

// runVerifier materializes the real diff to a temp file and spawns the read-only
// verifier, returning its parsed VERIFY result.
func runVerifier(issueKey string, proposal agent.Result, spawn SpawnFunc) (agent.Result, error) {
	diff, err := git.DiffText(proposal.Workspace, proposal.Base, proposal.Head)
	if err != nil {
		return agent.Result{}, err
	}
	f, err := os.CreateTemp("", "abf-diff-*.patch")
	if err != nil {
		return agent.Result{}, err
	}
	diffPath := f.Name()
	defer func() { _ = os.Remove(diffPath) }()
	if _, err := f.WriteString(diff); err != nil {
		_ = f.Close()
		return agent.Result{}, err
	}
	if err := f.Close(); err != nil {
		return agent.Result{}, err
	}
	return spawn(issueKey, PhaseVerify, map[string]string{
		EnvVerifyDiff: diffPath,
		EnvVerifyEvid: proposal.Evidence,
		EnvWorkspace:  proposal.Workspace,
	})
}

// downgrade rewrites an auto-fix proposal into a human-owned auto-diagnose with the
// reason recorded. Poller maps auto-diagnose to StatusWaiting. The MR was never
// opened (Phase B is skipped), so there is nothing to revoke.
func downgrade(res agent.Result, reason string) agent.Result {
	res.Outcome = agent.OutcomeAutoDiagnose
	res.MRURL = ""
	res.Verdict = agent.VerdictRefute
	res.VerifyReason = reason
	if res.HandoffPath == "" {
		res.HandoffPath = res.Evidence
	}
	return res
}

func verdictReason(res agent.Result) string {
	if strings.TrimSpace(res.VerifyReason) != "" {
		return "verifier refuted: " + res.VerifyReason
	}
	return "verifier refuted the evidence chain"
}

// withinRoot reports whether path is root or nested under it. It resolves symlinks
// on both sides so a junction/symlink inside root pointing outside cannot pass a
// string-only containment check; if the target path cannot be resolved (forged,
// dangling), it fails closed.
func withinRoot(root, path string) bool {
	r, err1 := filepath.Abs(root)
	p, err2 := filepath.Abs(path)
	if err1 != nil || err2 != nil {
		return false
	}
	if rr, err := filepath.EvalSymlinks(r); err == nil {
		r = rr // root is operator-controlled; if it can't resolve, keep the abs form
	}
	pr, err := filepath.EvalSymlinks(p)
	if err != nil {
		return false // agent-reported target must resolve to a real path
	}
	rel, err := filepath.Rel(r, pr)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
