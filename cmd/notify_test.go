package cmd

import (
	"strings"
	"testing"

	"github.com/fatecannotbealtered/auto-bug-fix/internal/config"
)

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

// With interaction enabled, a needs-info notify renders the interactive Card 2.0
// clarification card (input box + callback) instead of the one-way card.
func TestNotifyNeedsInfoRendersInteractiveCardWhenEnabled(t *testing.T) {
	t.Setenv("AUTO_BUG_FIX_INTERACT_ENABLED", "true")
	stdout, err := runCLIForTest(t, "notify",
		"--issue", "PROJ-7", "--outcome", "needs-info",
		"--summary", "登录报错", "--solution", "期望的行为是什么？", "--jira-url", "http://jira/PROJ-7",
		"--dry-run", "--compact")
	if err != nil {
		t.Fatalf("notify needs-info --dry-run failed: %v\n%s", err, stdout)
	}
	env := parseEnvelope(t, stdout)
	if !env.OK {
		t.Fatalf("notify --dry-run should succeed: %+v", env)
	}
	data := dataMap(t, env)
	preview, ok := data["preview"].(map[string]any)
	if !ok {
		t.Fatalf("preview should be a card object: %#v", data["preview"])
	}
	if preview["schema"] != "2.0" {
		t.Fatalf("interactive needs-info card must be Card 2.0, got schema %v", preview["schema"])
	}
}

// With interaction disabled, needs-info stays the one-way Card 1.0 (no schema key).
func TestNotifyNeedsInfoStaysOneWayWhenInteractDisabled(t *testing.T) {
	t.Setenv("AUTO_BUG_FIX_INTERACT_ENABLED", "")
	stdout, err := runCLIForTest(t, "notify",
		"--issue", "PROJ-7", "--outcome", "needs-info", "--solution", "q",
		"--dry-run", "--compact")
	if err != nil {
		t.Fatalf("notify needs-info --dry-run failed: %v\n%s", err, stdout)
	}
	data := dataMap(t, parseEnvelope(t, stdout))
	preview, _ := data["preview"].(map[string]any)
	if _, isCard2 := preview["schema"]; isCard2 {
		t.Fatal("with interaction disabled, needs-info must stay the one-way Card 1.0")
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

// TestNotifyRejectsNeedsReviewOutcome: needs-review is internal-only; the CLI
// must reject it so only the fix fallback can emit a degraded card.
func TestNotifyRejectsNeedsReviewOutcome(t *testing.T) {
	stdout, err := runCLIForTest(t, "notify", "--issue", "P-1", "--outcome", "needs-review", "--dry-run", "--compact")
	if err == nil {
		t.Fatal("notify --outcome needs-review must be rejected (internal-only)")
	}
	env := parseEnvelope(t, stdout)
	if env.OK || env.Error == nil || env.Error.Code != "E_VALIDATION" {
		t.Fatalf("expected E_VALIDATION, got %+v", env)
	}
}

// TestSendFallbackCard_RendersNeedsReviewAndSends: the in-process fallback sends
// the degraded needs-review card to the configured recipient via the runner.
func TestSendFallbackCard_RendersNeedsReviewAndSends(t *testing.T) {
	cfg := config.Config{Notify: config.NotifyConfig{Enabled: true, Channel: "lark", Target: "ou_test"}}
	var gotContent, gotRecipient string
	run := func(_ string, args ...string) ([]byte, error) {
		for i, a := range args {
			if a == "--content" && i+1 < len(args) {
				gotContent = args[i+1]
			}
			if (a == "--user-id" || a == "--chat-id") && i+1 < len(args) {
				gotRecipient = args[i+1]
			}
		}
		return []byte(`{"ok":true,"data":{"message_id":"om_fallback"}}`), nil
	}
	if err := sendFallbackCard(cfg, "ZNZS-1341", run); err != nil {
		t.Fatalf("fallback send should succeed: %v", err)
	}
	if gotRecipient != "ou_test" {
		t.Errorf("recipient: got %q, want ou_test", gotRecipient)
	}
	if !strings.Contains(gotContent, "ZNZS-1341") {
		t.Errorf("card should reference the issue, got %s", gotContent)
	}
	if !strings.Contains(gotContent, "核对") {
		t.Errorf("card should be the needs-review degraded card, got %s", gotContent)
	}
}

// TestSendFallbackCard_NoRecipientErrors: without a configured recipient the
// fallback returns an error and never attempts a send.
func TestSendFallbackCard_NoRecipientErrors(t *testing.T) {
	t.Setenv("AUTO_BUG_FIX_NOTIFY_TARGET", "")
	cfg := config.Config{Notify: config.NotifyConfig{Enabled: true, Channel: "lark"}}
	called := false
	run := func(string, ...string) ([]byte, error) { called = true; return nil, nil }
	if err := sendFallbackCard(cfg, "P-1", run); err == nil {
		t.Fatal("expected error when no recipient is configured")
	}
	if called {
		t.Error("must not attempt a send without a recipient")
	}
}
