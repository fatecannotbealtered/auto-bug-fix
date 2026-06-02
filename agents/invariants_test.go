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
	templates := map[string]string{
		"kiro":        "kiro/auto-bug-fix.md",
		"claude-code": "claude-code/auto-bug-fix.md",
		"cursor":      "cursor/auto-bug-fix.mdc",
		"codex":       "codex/AGENTS.md",
	}

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
		{"empty result is a symptom", []string{"symptom, not a root cause"}},
		{"observability-first on missing signal", []string{"observability gap"}},
		{"external contract confirmed not inferred", []string{"confirmed, not inferred"}},
		{"no catch-all masking distinct causes", []string{"masks distinct causes"}},
		{"never merge MRs", []string{"merge MRs", "merge the MR"}},
		{"never push to default branch", []string{"default branch"}},
		{"localized-change ceiling", []string{"more than 5 files", "5 files"}},
		{"business-language Jira root-cause", []string{"【问题原因】"}},
		{"business-language Jira solution", []string{"【解决方案】"}},
	}

	for agentName, path := range templates {
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
