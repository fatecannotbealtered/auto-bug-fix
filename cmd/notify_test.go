package cmd

import "testing"

// Command-level test for the `notify` leaf (also satisfies the FCC guard).
// --dry-run renders the card without sending, so it needs no lark-cli or recipient.
func TestNotifyDryRunRendersPreview(t *testing.T) {
	stdout, err := runCLIForTest(t, "notify",
		"--issue", "PROJ-1", "--outcome", "auto-fix",
		"--summary", "demo", "--root-cause", "rc", "--solution", "sol",
		"--mr-url", "http://mr", "--jira-url", "http://jira",
		"--dry-run", "--compact")
	if err != nil {
		t.Fatalf("notify --dry-run failed: %v\n%s", err, stdout)
	}
	env := parseEnvelope(t, stdout)
	if !env.OK {
		t.Fatalf("notify --dry-run should succeed: %+v", env)
	}
	data := dataMap(t, env)
	if data["wouldSend"] != true {
		t.Fatalf("dry-run should report wouldSend=true: %#v", data)
	}
	if _, ok := data["preview"]; !ok {
		t.Fatalf("dry-run should include a rendered preview: %#v", data)
	}
}

func TestNotifyInvalidOutcomeIsValidationError(t *testing.T) {
	stdout, err := runCLIForTest(t, "notify", "--issue", "PROJ-1", "--outcome", "bogus", "--dry-run", "--compact")
	if err == nil {
		t.Fatalf("notify with a bad outcome should fail")
	}
	env := parseEnvelope(t, stdout)
	if env.OK || env.Error == nil || env.Error.Code != "E_VALIDATION" {
		t.Fatalf("expected E_VALIDATION envelope, got %+v", env)
	}
}
