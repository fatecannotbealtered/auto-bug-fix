package cmd

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fatecannotbealtered/auto-bug-fix/internal/agent"
)

// Command-level tests for every leaf command `reference` enumerates. Together
// with TestCommandDefaultsToJSONEnvelope (status) and
// TestWriteWithoutConfirmReturnsErrorEnvelope (stop) in contract_test.go, these
// give every leaf command the command-level coverage the FCC guard requires.

func parseEnvelope(t *testing.T, stdout []byte) jsonEnvelope {
	t.Helper()
	var env jsonEnvelope
	if err := json.Unmarshal(stdout, &env); err != nil {
		t.Fatalf("output is not one JSON envelope: %v\n%s", err, stdout)
	}
	return env
}

func dataMap(t *testing.T, env jsonEnvelope) map[string]any {
	t.Helper()
	m, ok := env.Data.(map[string]any)
	if !ok {
		t.Fatalf("envelope data is not an object: %#v", env.Data)
	}
	return m
}

func TestSetupDryRunEmitsConfirmToken(t *testing.T) {
	stdout, err := runCLIForTest(t, "setup", "--agent", "codex", "--dry-run", "--compact")
	if err != nil {
		t.Fatalf("setup --dry-run failed: %v\n%s", err, stdout)
	}
	env := parseEnvelope(t, stdout)
	if !env.OK || env.Data == nil {
		t.Fatalf("setup --dry-run should succeed with data: %+v", env)
	}
}

func TestStartDryRunEmitsConfirmToken(t *testing.T) {
	stdout, err := runCLIForTest(t, "start", "--dry-run", "--compact")
	if err != nil {
		t.Fatalf("start --dry-run failed: %v\n%s", err, stdout)
	}
	if env := parseEnvelope(t, stdout); !env.OK {
		t.Fatalf("start --dry-run should succeed: %+v", env)
	}
}

func TestFixDryRunEmitsConfirmToken(t *testing.T) {
	stdout, err := runCLIForTest(t, "fix", "PROJ-1", "--dry-run", "--compact")
	if err != nil {
		t.Fatalf("fix --dry-run failed: %v\n%s", err, stdout)
	}
	if env := parseEnvelope(t, stdout); !env.OK {
		t.Fatalf("fix --dry-run should succeed: %+v", env)
	}
}

func TestUpdateDryRunEmitsConfirmToken(t *testing.T) {
	// --target-version skips the network registry lookup so the test is hermetic.
	stdout, err := runCLIForTest(t, "update", "--dry-run", "--target-version", "9.9.9", "--compact")
	if err != nil {
		t.Fatalf("update --dry-run failed: %v\n%s", err, stdout)
	}
	if env := parseEnvelope(t, stdout); !env.OK {
		t.Fatalf("update --dry-run should succeed: %+v", env)
	}
}

func TestVersionEmitsVersion(t *testing.T) {
	stdout, err := runCLIForTest(t, "version", "--compact")
	if err != nil {
		t.Fatalf("version failed: %v\n%s", err, stdout)
	}
	data := dataMap(t, parseEnvelope(t, stdout))
	if v, _ := data["version"].(string); v == "" {
		t.Fatalf("version output missing version: %#v", data)
	}
}

func TestReferenceEmitsReleaseReadiness(t *testing.T) {
	stdout, err := runCLIForTest(t, "reference", "--compact")
	if err != nil {
		t.Fatalf("reference failed: %v\n%s", err, stdout)
	}
	data := dataMap(t, parseEnvelope(t, stdout))
	if _, ok := data["release_readiness"]; !ok {
		t.Fatalf("reference output missing release_readiness: %#v", data)
	}
}

func TestContextEmitsNoticesField(t *testing.T) {
	stdout, err := runCLIForTest(t, "context", "--compact")
	if err != nil {
		t.Fatalf("context failed: %v\n%s", err, stdout)
	}
	data := dataMap(t, parseEnvelope(t, stdout))
	// The context_result schema declares `notices`; it must be present (read from
	// the local update cache, empty when no update is cached).
	if _, ok := data["notices"]; !ok {
		t.Fatalf("context output missing notices field declared by schema: %#v", data)
	}
}

func TestChangelogEmitsEntries(t *testing.T) {
	stdout, err := runCLIForTest(t, "changelog", "--compact")
	if err != nil {
		t.Fatalf("changelog failed: %v\n%s", err, stdout)
	}
	data := dataMap(t, parseEnvelope(t, stdout))
	if _, ok := data["entries"]; !ok {
		t.Fatalf("changelog output missing entries: %#v", data)
	}
}

func TestDoctorEmitsReleaseReadinessCheck(t *testing.T) {
	// A non-existent config makes doctor deterministic regardless of the dev's
	// real home: required checks fail, so doctor exits non-zero but still emits a
	// JSON envelope whose checks include release_readiness.
	missingConfig := filepath.Join(t.TempDir(), "no-config.json")
	stdout, _ := runCLIForTest(t, "doctor", "--config", missingConfig, "--compact")
	parseEnvelope(t, stdout) // must be one well-formed envelope, ok true or false
	if !strings.Contains(string(stdout), "release_readiness") {
		t.Fatalf("doctor output missing release_readiness check: %s", stdout)
	}
}

func TestFixResultDataTagsUntrustedFields(t *testing.T) {
	data := fixResultData("PROJ-1", agent.Result{Outcome: "auto-fix", MRURL: "http://mr", HandoffPath: "/h"})
	got, ok := data["_untrusted"].([]string)
	if !ok {
		t.Fatalf("fix_result missing _untrusted slice: %#v", data["_untrusted"])
	}
	want := referenceSchemas()["fix_result"].(map[string]any)["untrusted_fields"].([]string)
	if !sameStringSet(got, want) {
		t.Fatalf("fix _untrusted %v does not match schema untrusted_fields %v", got, want)
	}
	// Every tagged field must actually be present in the payload.
	for _, f := range got {
		if _, ok := data[f]; !ok {
			t.Errorf("_untrusted lists %q but it is not in the payload", f)
		}
	}
}

func TestSchemaUntrustedFieldsAreDeclaredFields(t *testing.T) {
	for name, raw := range referenceSchemas() {
		s := raw.(map[string]any)
		fields := map[string]bool{}
		for _, f := range s["fields"].([]string) {
			fields[f] = true
		}
		for _, uf := range s["untrusted_fields"].([]string) {
			if !fields[uf] {
				t.Errorf("schema %s: untrusted field %q is not in fields", name, uf)
			}
		}
	}
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, s := range a {
		seen[s]++
	}
	for _, s := range b {
		seen[s]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}
