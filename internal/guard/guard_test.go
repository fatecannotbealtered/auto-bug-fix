package guard_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/fatecannotbealtered/auto-bug-fix/internal/agent"
	"github.com/fatecannotbealtered/auto-bug-fix/internal/git"
	"github.com/fatecannotbealtered/auto-bug-fix/internal/guard"
)

type fakeSpawn struct {
	mu      sync.Mutex
	phases  []guard.Phase
	results map[guard.Phase]agent.Result
	errs    map[guard.Phase]error
}

func newFake() *fakeSpawn {
	return &fakeSpawn{results: map[guard.Phase]agent.Result{}, errs: map[guard.Phase]error{}}
}

func (f *fakeSpawn) spawn(_ string, phase guard.Phase, _ map[string]string) (agent.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.phases = append(f.phases, phase)
	return f.results[phase], f.errs[phase]
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-C", dir}, args...)
	cmd := exec.Command("git", full...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// setupRepo builds base + fix commit and returns dir, base SHA, fix branch, head SHA.
func setupRepo(t *testing.T) (dir, base, branch, head string) {
	t.Helper()
	dir = t.TempDir()
	gitRun(t, dir, "init", "-q")
	// Real evidence file so the integrity check's withinRoot (which now resolves
	// symlinks and requires the path to exist) does not fail closed on it.
	if err := os.MkdirAll(filepath.Join(dir, ".auto-bug-fix"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".auto-bug-fix", "evidence.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-q", "-m", "base")
	base, _ = git.HeadSHA(dir)

	branch = "fix/PROJ-1-thing"
	gitRun(t, dir, "checkout", "-q", "-b", branch)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-q", "-m", "fix")
	head, _ = git.HeadSHA(dir)
	return dir, base, branch, head
}

func proposal(dir, base, branch, head string) agent.Result {
	return agent.Result{
		MarkerKind: agent.MarkerProposal,
		Outcome:    agent.OutcomeAutoFix,
		Workspace:  dir,
		Base:       base,
		Branch:     branch,
		Head:       head,
		Evidence:   filepath.Join(dir, ".auto-bug-fix", "evidence.json"),
	}
}

func TestRunGuarded_DisabledRunsFullOnce(t *testing.T) {
	f := newFake()
	f.results[guard.PhaseFull] = agent.Result{MarkerKind: agent.MarkerResult, Outcome: agent.OutcomeAutoFix, MRURL: "https://x/1"}
	res, err := guard.RunGuarded("PROJ-1", guard.Config{Enabled: false}, f.spawn)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.phases) != 1 || f.phases[0] != guard.PhaseFull {
		t.Fatalf("phases: got %v, want [full]", f.phases)
	}
	if res.MRURL != "https://x/1" {
		t.Fatalf("mr: got %q", res.MRURL)
	}
}

func TestRunGuarded_TerminalProposalPassesThrough(t *testing.T) {
	f := newFake()
	f.results[guard.PhaseInvestigate] = agent.Result{MarkerKind: agent.MarkerResult, Outcome: agent.OutcomeAutoDiagnose, HandoffPath: "h.md"}
	res, err := guard.RunGuarded("PROJ-1", guard.Config{Enabled: true}, f.spawn)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.phases) != 1 || f.phases[0] != guard.PhaseInvestigate {
		t.Fatalf("phases: got %v, want [investigate]", f.phases)
	}
	if res.Outcome != agent.OutcomeAutoDiagnose || res.HandoffPath != "h.md" {
		t.Fatalf("expected passthrough auto-diagnose, got %+v", res)
	}
}

func TestRunGuarded_UpholdRunsExecute(t *testing.T) {
	dir, base, branch, head := setupRepo(t)
	f := newFake()
	f.results[guard.PhaseInvestigate] = proposal(dir, base, branch, head)
	f.results[guard.PhaseVerify] = agent.Result{MarkerKind: agent.MarkerVerify, Verdict: agent.VerdictUphold}
	f.results[guard.PhaseExecute] = agent.Result{MarkerKind: agent.MarkerResult, Outcome: agent.OutcomeAutoFix, MRURL: "https://x/mr/9"}

	res, err := guard.RunGuarded("PROJ-1", guard.Config{Enabled: true, WorkspaceRoot: filepath.Dir(dir)}, f.spawn)
	if err != nil {
		t.Fatal(err)
	}
	want := []guard.Phase{guard.PhaseInvestigate, guard.PhaseVerify, guard.PhaseExecute}
	if !equalPhases(f.phases, want) {
		t.Fatalf("phases: got %v, want %v", f.phases, want)
	}
	if res.Outcome != agent.OutcomeAutoFix || res.MRURL != "https://x/mr/9" {
		t.Fatalf("expected executed auto-fix, got %+v", res)
	}
}

func TestRunGuarded_RefuteDowngradesAndSkipsExecute(t *testing.T) {
	dir, base, branch, head := setupRepo(t)
	f := newFake()
	f.results[guard.PhaseInvestigate] = proposal(dir, base, branch, head)
	f.results[guard.PhaseVerify] = agent.Result{MarkerKind: agent.MarkerVerify, Verdict: agent.VerdictRefute, VerifyReason: "evidence is only a self-written test"}

	res, err := guard.RunGuarded("PROJ-1", guard.Config{Enabled: true, WorkspaceRoot: filepath.Dir(dir)}, f.spawn)
	if err != nil {
		t.Fatal(err)
	}
	want := []guard.Phase{guard.PhaseInvestigate, guard.PhaseVerify}
	if !equalPhases(f.phases, want) {
		t.Fatalf("phases: got %v, want %v (execute must be skipped)", f.phases, want)
	}
	if res.Outcome != agent.OutcomeAutoDiagnose {
		t.Fatalf("expected downgrade to auto-diagnose, got %q", res.Outcome)
	}
	if res.MRURL != "" {
		t.Fatalf("downgrade must clear MR, got %q", res.MRURL)
	}
	if res.Verdict != agent.VerdictRefute || res.VerifyReason == "" {
		t.Fatalf("expected refute verdict + reason, got %+v", res)
	}
}

func TestRunGuarded_IntegrityFailDowngradesBeforeVerify(t *testing.T) {
	dir, base, branch, _ := setupRepo(t)
	f := newFake()
	// Report a HEAD that does not match the real checkout.
	bad := proposal(dir, base, branch, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	f.results[guard.PhaseInvestigate] = bad

	res, err := guard.RunGuarded("PROJ-1", guard.Config{Enabled: true, WorkspaceRoot: filepath.Dir(dir)}, f.spawn)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.phases) != 1 || f.phases[0] != guard.PhaseInvestigate {
		t.Fatalf("phases: got %v, want [investigate] (verify/execute must be skipped)", f.phases)
	}
	if res.Outcome != agent.OutcomeAutoDiagnose || res.Verdict != agent.VerdictRefute {
		t.Fatalf("expected integrity downgrade, got %+v", res)
	}
}

func TestRunGuarded_WorkspaceOutsideRootDowngrades(t *testing.T) {
	dir, base, branch, head := setupRepo(t)
	f := newFake()
	f.results[guard.PhaseInvestigate] = proposal(dir, base, branch, head)
	// Root is an unrelated directory; the proposal's workspace is not under it.
	res, err := guard.RunGuarded("PROJ-1", guard.Config{Enabled: true, WorkspaceRoot: t.TempDir()}, f.spawn)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != agent.OutcomeAutoDiagnose {
		t.Fatalf("expected downgrade for out-of-root workspace, got %q", res.Outcome)
	}
	if len(f.phases) != 1 {
		t.Fatalf("verify/execute must be skipped, phases=%v", f.phases)
	}
}

// TestRunGuarded_IntegrityFieldsDowngrade covers the checkIntegrity downgrade
// branches: each proposal stops at investigate, downgrades to auto-diagnose/refute,
// and never reaches verify or execute.
func TestRunGuarded_IntegrityFieldsDowngrade(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(p *agent.Result)
	}{
		{"empty branch", func(p *agent.Result) { p.Branch = "" }},
		{"missing workspace", func(p *agent.Result) { p.Workspace = "" }},
		{"missing base", func(p *agent.Result) { p.Base = "" }},
		{"missing head", func(p *agent.Result) { p.Head = "" }},
		// Base == Head => diff base...head touches 0 files => empty-diff downgrade.
		{"empty diff", func(p *agent.Result) { p.Base = p.Head }},
		// Evidence points outside the workspace at a path that does not exist.
		{"evidence outside workspace", func(p *agent.Result) {
			p.Evidence = filepath.Join(filepath.Dir(p.Workspace), "outside-evidence.json")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, base, branch, head := setupRepo(t)
			f := newFake()
			p := proposal(dir, base, branch, head)
			tc.mutate(&p)
			f.results[guard.PhaseInvestigate] = p

			res, err := guard.RunGuarded("PROJ-1", guard.Config{Enabled: true, WorkspaceRoot: filepath.Dir(dir)}, f.spawn)
			if err != nil {
				t.Fatal(err)
			}
			if !equalPhases(f.phases, []guard.Phase{guard.PhaseInvestigate}) {
				t.Fatalf("phases: got %v, want [investigate] (verify/execute must be skipped)", f.phases)
			}
			if res.Outcome != agent.OutcomeAutoDiagnose || res.Verdict != agent.VerdictRefute {
				t.Fatalf("expected integrity downgrade, got %+v", res)
			}
		})
	}
}

func TestRunGuarded_ApprovalHoldsBeforeExecute(t *testing.T) {
	dir, base, branch, head := setupRepo(t)
	f := newFake()
	f.results[guard.PhaseInvestigate] = proposal(dir, base, branch, head)
	f.results[guard.PhaseVerify] = agent.Result{MarkerKind: agent.MarkerVerify, Verdict: agent.VerdictUphold}
	f.results[guard.PhaseExecute] = agent.Result{MarkerKind: agent.MarkerResult, Outcome: agent.OutcomeAutoFix, MRURL: "https://x/mr/should-not-happen"}

	cfg := guard.Config{Enabled: true, WorkspaceRoot: filepath.Dir(dir), RequireApproval: true}
	res, err := guard.RunGuarded("PROJ-1", cfg, f.spawn)
	if err != nil {
		t.Fatal(err)
	}
	want := []guard.Phase{guard.PhaseInvestigate, guard.PhaseVerify}
	if !equalPhases(f.phases, want) {
		t.Fatalf("phases: got %v, want %v (execute must be held for approval)", f.phases, want)
	}
	if !res.AwaitingApproval {
		t.Fatal("upheld proposal under approval gate must set AwaitingApproval")
	}
	if res.MRURL != "" {
		t.Fatalf("no MR should exist while awaiting approval, got %q", res.MRURL)
	}
	if res.Head != head || res.Workspace != dir || res.Branch != branch || res.Base != base {
		t.Fatalf("proposal fields must survive for a later Execute, got %+v", res)
	}
}

func TestExecute_ResumesPhaseBOnApprove(t *testing.T) {
	dir, base, branch, head := setupRepo(t)
	f := newFake()
	f.results[guard.PhaseExecute] = agent.Result{MarkerKind: agent.MarkerResult, Outcome: agent.OutcomeAutoFix, MRURL: "https://x/mr/42"}
	cfg := guard.Config{Enabled: true, WorkspaceRoot: filepath.Dir(dir), RequireApproval: true}

	res, err := guard.Execute("PROJ-1", cfg, proposal(dir, base, branch, head), f.spawn)
	if err != nil {
		t.Fatal(err)
	}
	if !equalPhases(f.phases, []guard.Phase{guard.PhaseExecute}) {
		t.Fatalf("Execute should run only Phase B, got %v", f.phases)
	}
	if res.MRURL != "https://x/mr/42" {
		t.Fatalf("expected executed MR, got %+v", res)
	}
}

func TestExecute_IntegrityRecheckDowngrades(t *testing.T) {
	dir, base, branch, _ := setupRepo(t)
	f := newFake()
	// HEAD moved / mismatched since verification: Execute must refuse and downgrade.
	bad := proposal(dir, base, branch, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	cfg := guard.Config{Enabled: true, WorkspaceRoot: filepath.Dir(dir), RequireApproval: true}

	res, err := guard.Execute("PROJ-1", cfg, bad, f.spawn)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.phases) != 0 {
		t.Fatalf("execute must be skipped on integrity re-check failure, got %v", f.phases)
	}
	if res.Outcome != agent.OutcomeAutoDiagnose || res.Verdict != agent.VerdictRefute {
		t.Fatalf("expected integrity-recheck downgrade, got %+v", res)
	}
}

func equalPhases(a, b []guard.Phase) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
