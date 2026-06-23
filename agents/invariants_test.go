package agents

import (
	"strings"
	"testing"
)

// architecture.md states the invariant across agent templates is "behavioral
// equivalence, not byte-for-byte identity". Wording may differ per tool, but the
// load-bearing policy must appear in every surface. This test guards that
// invariant so editing one template and forgetting the others fails loudly
// instead of silently drifting in production.
func TestAgentTemplates_ShareLoadBearingPolicy(t *testing.T) {
	// Each entry is a human-readable label and the substrings that prove the
	// policy is present. Multiple substrings mean "any of these counts" — that
	// tolerates wording differences while still requiring the rule to exist.
	invariants := []struct {
		name  string
		anyOf []string
	}{
		{"audit marker", []string{"AUTO_BUG_FIX_RESULT"}},
		{"outcome auto-fix", []string{"auto-fix"}},
		{"outcome auto-diagnose", []string{"auto-diagnose"}},
		{"outcome needs-info", []string{"needs-info"}},
		{"confidence gate", []string{"Confidence Gate", "Confidence gate"}},
		{"triage defect vs spec-gap before cloning", []string{"spec-gap"}},
		{"empty result is a symptom", []string{"symptom, not a root cause"}},
		{"observability-first on missing signal", []string{"observability gap"}},
		{"external contract confirmed not inferred", []string{"confirmed, not inferred"}},
		{"no catch-all masking distinct causes", []string{"masks distinct causes"}},
		{"caveats: load-bearing code may look wrong", []string{"load-bearing", "caveat"}},
		{"archery is read-only SELECT-only diagnostic", []string{"SELECT only", "read-only database account"}},
		// Orchestration policies: auto-bug-fix's own procedure/safety, independent of
		// any CLI's exact flags. Kept hard-coded even though exact CLI syntax now
		// defers to each CLI's skill (see TestAgentTemplates_NoStaleCLISyntax).
		{"load the CLI skill before driving it", []string{"load its skill", "load the matching skill", "consult the matching skill"}},
		{"resolve full namespaced path (a bare name 404s)", []string{"full namespaced path", "bare name"}},
		{"idempotent MR — never duplicate", []string{"idempoten", "duplicate"}},
		{"do not assume main; target the base branch", []string{"assume `main`", "base branch"}},
		{"writes preview with dry-run then confirm", []string{"dry-run"}},
		{"never merge MRs", []string{"merge MRs", "merge the MR"}},
		{"never push to default branch", []string{"default branch"}},
		{"localized-change ceiling", []string{"more than 5 files", "5 files"}},
		{"business-language Jira root-cause", []string{"【问题原因】"}},
		{"business-language Jira solution", []string{"【解决方案】"}},
		{"completion notification step (Lark) when enabled", []string{"AUTO_BUG_FIX_NOTIFY", "completion notification"}},
	}

	for agentName, path := range agentTemplates() {
		content, err := ReadFile(splitDir(path))
		if err != nil {
			t.Fatalf("%s: read template: %v", agentName, err)
		}
		for _, inv := range invariants {
			if !containsAny(content, inv.anyOf) {
				t.Errorf("%s template (%s) is missing load-bearing policy %q (expected one of %v)",
					agentName, path, inv.name, inv.anyOf)
			}
		}
	}
}

// TestAgentTemplates_NoStaleCLISyntax guards against stale or wrong CLI syntax
// leaking into the templates. Exact, current CLI syntax (subcommands, flags, JSON
// fields) is owned by each CLI's own skill and is intentionally NOT pinned here —
// the templates defer to the skill. This test only blocks known-bad patterns from
// reappearing; each entry encodes a real past regression.
func TestAgentTemplates_NoStaleCLISyntax(t *testing.T) {
	forbidden := []struct {
		pattern string
		lesson  string
	}{
		{"--last", "stale kibana flag; use a date-window like --from"},
		{"default_branch", "wrong snake_case GitLab field; the CLI uses camelCase"},
		{"read `.fields.description`", "wrong Jira field as primary; the CLI's .data payload is canonical"},
		{"--project <serviceName>", "bare project name 404s; resolve to the full namespaced path first"},
	}

	for agentName, path := range agentTemplates() {
		content, err := ReadFile(splitDir(path))
		if err != nil {
			t.Fatalf("%s: read template: %v", agentName, err)
		}
		for _, bad := range forbidden {
			if strings.Contains(content, bad.pattern) {
				t.Errorf("%s template (%s) contains stale CLI pattern %q (%s)", agentName, path, bad.pattern, bad.lesson)
			}
		}
	}
}

func agentTemplates() map[string]string {
	return map[string]string{
		"kiro":        "kiro/auto-bug-fix.md",
		"claude-code": "claude-code/auto-bug-fix.md",
		"cursor":      "cursor/auto-bug-fix.mdc",
		"codex":       "codex/AGENTS.md",
	}
}

func containsAny(haystack string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(haystack, n) {
			return true
		}
	}
	return false
}

// splitDir turns "kiro/SKILL.md" into the (agentType, filename) pair ReadFile expects.
func splitDir(path string) (string, string) {
	i := strings.IndexByte(path, '/')
	return path[:i], path[i+1:]
}
