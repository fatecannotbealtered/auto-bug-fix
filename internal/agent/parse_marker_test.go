package agent_test

import (
	"testing"

	"github.com/fatecannotbealtered/auto-bug-fix/internal/agent"
)

func TestParseMarker(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want agent.Result
	}{
		{
			name: "proposal with all fields",
			in:   "log\nAUTO_BUG_FIX_PROPOSAL outcome=auto-fix workspace=/ws/repo branch=fix/PROJ-1-x base=develop head=abc123 evidence=/ws/repo/.auto-bug-fix/evidence.json\n",
			want: agent.Result{
				MarkerKind: agent.MarkerProposal, Outcome: "auto-fix",
				Workspace: "/ws/repo", Branch: "fix/PROJ-1-x", Base: "develop", Head: "abc123",
				Evidence: "/ws/repo/.auto-bug-fix/evidence.json",
			},
		},
		{
			name: "verify uphold with multi-word reason captured to end of line",
			in:   "AUTO_BUG_FIX_VERIFY verdict=uphold reason=runtime log confirms the null path\n",
			want: agent.Result{MarkerKind: agent.MarkerVerify, Verdict: "uphold", VerifyReason: "runtime log confirms the null path"},
		},
		{
			name: "verify refute",
			in:   "AUTO_BUG_FIX_VERIFY verdict=refute reason=evidence is only a self-written test\n",
			want: agent.Result{MarkerKind: agent.MarkerVerify, Verdict: "refute", VerifyReason: "evidence is only a self-written test"},
		},
		{
			name: "terminal result auto-fix",
			in:   "AUTO_BUG_FIX_RESULT outcome=auto-fix mr=https://gitlab.example/mr/1\n",
			want: agent.Result{MarkerKind: agent.MarkerResult, Outcome: "auto-fix", MRURL: "https://gitlab.example/mr/1"},
		},
		{
			name: "result auto-diagnose with handoff",
			in:   "AUTO_BUG_FIX_RESULT outcome=auto-diagnose handoff=.repo-knowledge/handoff/PROJ-1.needs-confirmation.md\n",
			want: agent.Result{MarkerKind: agent.MarkerResult, Outcome: "auto-diagnose", HandoffPath: ".repo-knowledge/handoff/PROJ-1.needs-confirmation.md"},
		},
		{
			name: "placeholder line with angle brackets is ignored",
			in:   "AUTO_BUG_FIX_PROPOSAL outcome=auto-fix head=<commit-sha>\n",
			want: agent.Result{MarkerKind: agent.MarkerNone},
		},
		{
			name: "last marker line wins",
			in:   "AUTO_BUG_FIX_PROPOSAL outcome=auto-fix head=abc\nAUTO_BUG_FIX_RESULT outcome=auto-fix mr=https://x/2\n",
			want: agent.Result{MarkerKind: agent.MarkerResult, Outcome: "auto-fix", MRURL: "https://x/2"},
		},
		{
			name: "no marker",
			in:   "just some logs\nnothing to see\n",
			want: agent.Result{MarkerKind: agent.MarkerNone},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := agent.ParseMarker(tc.in)
			if got.MarkerKind != tc.want.MarkerKind {
				t.Fatalf("kind: got %q, want %q", got.MarkerKind, tc.want.MarkerKind)
			}
			if got.Outcome != tc.want.Outcome || got.MRURL != tc.want.MRURL || got.HandoffPath != tc.want.HandoffPath {
				t.Errorf("outcome/mr/handoff: got %+v, want %+v", got, tc.want)
			}
			if got.Workspace != tc.want.Workspace || got.Branch != tc.want.Branch || got.Base != tc.want.Base || got.Head != tc.want.Head || got.Evidence != tc.want.Evidence {
				t.Errorf("proposal fields: got %+v, want %+v", got, tc.want)
			}
			if got.Verdict != tc.want.Verdict || got.VerifyReason != tc.want.VerifyReason {
				t.Errorf("verify fields: got verdict=%q reason=%q, want verdict=%q reason=%q", got.Verdict, got.VerifyReason, tc.want.Verdict, tc.want.VerifyReason)
			}
		})
	}
}

// TestParseResultMarker_IgnoresNonResultMarkers guards the back-compat wrapper:
// it must only react to a terminal RESULT line, not to a PROPOSAL/VERIFY line.
func TestParseResultMarker_IgnoresNonResultMarkers(t *testing.T) {
	r := agent.ParseResultMarker("AUTO_BUG_FIX_PROPOSAL outcome=auto-fix head=abc\n")
	if r.MarkerKind != agent.MarkerNone || r.Outcome != "" {
		t.Fatalf("ParseResultMarker must ignore a PROPOSAL line, got %+v", r)
	}
}
