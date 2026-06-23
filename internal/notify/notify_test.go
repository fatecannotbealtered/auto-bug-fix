package notify_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/fatecannotbealtered/auto-bug-fix/internal/notify"
)

func headerTemplate(t *testing.T, cardJSON string) string {
	t.Helper()
	var card struct {
		Header struct {
			Template string `json:"template"`
			Title    struct {
				Content string `json:"content"`
			} `json:"title"`
		} `json:"header"`
	}
	if err := json.Unmarshal([]byte(cardJSON), &card); err != nil {
		t.Fatalf("card is not valid JSON: %v\n%s", err, cardJSON)
	}
	return card.Header.Template
}

func TestRenderCard_AutoFixIsGreenWithMRButton(t *testing.T) {
	card, err := notify.RenderCard(notify.Params{
		Issue: "PROJ-1234", Outcome: notify.OutcomeAutoFix, Summary: "导出为空",
		RootCause: "边界未含", Solution: "改左闭右闭", TestStatus: "fail→pass",
		MRURL: "https://gl/mr/87", JiraURL: "https://jira/PROJ-1234",
		Service: "order-svc", Branch: "fix/x", Duration: "3m",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := headerTemplate(t, card); got != "green" {
		t.Errorf("auto-fix header should be green, got %q", got)
	}
	for _, want := range []string{"评审 MR", "https://gl/mr/87", "Jira 工单", "问题原因", "解决方案", "🧪", "order-svc", "fix/x"} {
		if !strings.Contains(card, want) {
			t.Errorf("auto-fix card missing %q", want)
		}
	}
}

func TestRenderCard_NeedsInfoIsBlueWithNoMRButton(t *testing.T) {
	// Even if an MR URL is (wrongly) supplied, needs-info must not render an MR button.
	card, err := notify.RenderCard(notify.Params{
		Issue: "PROJ-9", Outcome: notify.OutcomeNeedsInfo, Solution: "1. ? 2. ?",
		MRURL: "https://should/not/appear", JiraURL: "https://jira/PROJ-9",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := headerTemplate(t, card); got != "blue" {
		t.Errorf("needs-info header should be blue, got %q", got)
	}
	if strings.Contains(card, "评审 MR") || strings.Contains(card, "should/not/appear") {
		t.Errorf("needs-info card must not contain an MR button: %s", card)
	}
	if !strings.Contains(card, "去 Jira 回复") {
		t.Errorf("needs-info card should have the Jira reply CTA")
	}
	if !strings.Contains(card, "待确认") {
		t.Errorf("needs-info solution label should be 待确认")
	}
}

func TestRenderCard_AutoDiagnoseIsOrange(t *testing.T) {
	card, err := notify.RenderCard(notify.Params{Issue: "P-2", Outcome: notify.OutcomeAutoDiagnose, JiraURL: "https://jira/P-2"})
	if err != nil {
		t.Fatal(err)
	}
	if got := headerTemplate(t, card); got != "orange" {
		t.Errorf("auto-diagnose header should be orange, got %q", got)
	}
	if strings.Contains(card, "评审 MR") {
		t.Errorf("auto-diagnose must not render an MR button")
	}
}

func TestRenderCard_RejectsUnknownOutcome(t *testing.T) {
	if _, err := notify.RenderCard(notify.Params{Issue: "P-1", Outcome: "bogus"}); err == nil {
		t.Fatal("expected error for unknown outcome")
	}
}

func TestSend_ParsesMessageIDAndPicksUserID(t *testing.T) {
	var gotArgs []string
	run := func(args ...string) ([]byte, error) {
		gotArgs = args
		return []byte(`{"ok":true,"data":{"message_id":"om_abc","chat_id":"oc_def"}}`), nil
	}
	msgID, chatID, err := notify.Send("ou_user1", `{"x":1}`, run)
	if err != nil {
		t.Fatal(err)
	}
	if msgID != "om_abc" || chatID != "oc_def" {
		t.Fatalf("parsed wrong ids: %q %q", msgID, chatID)
	}
	if !contains(gotArgs, "--user-id") {
		t.Errorf("ou_ recipient should use --user-id, got %v", gotArgs)
	}
}

func TestSend_PicksChatIDForOC(t *testing.T) {
	var gotArgs []string
	run := func(args ...string) ([]byte, error) {
		gotArgs = args
		return []byte(`{"ok":true,"data":{"message_id":"om_1"}}`), nil
	}
	if _, _, err := notify.Send("oc_group1", "{}", run); err != nil {
		t.Fatal(err)
	}
	if !contains(gotArgs, "--chat-id") {
		t.Errorf("oc_ recipient should use --chat-id, got %v", gotArgs)
	}
}

func TestSend_OKFalseIsError(t *testing.T) {
	run := func(args ...string) ([]byte, error) {
		return []byte(`{"ok":false,"error":{"message":"chat not found"}}`), nil
	}
	if _, _, err := notify.Send("ou_x", "{}", run); err == nil || !strings.Contains(err.Error(), "chat not found") {
		t.Fatalf("expected ok:false error to surface, got %v", err)
	}
}

func TestSend_EmptyRecipientIsError(t *testing.T) {
	if _, _, err := notify.Send("", "{}", func(...string) ([]byte, error) { return nil, nil }); err == nil {
		t.Fatal("expected error for empty recipient")
	}
}

func contains(s []string, want string) bool {
	for _, v := range s {
		if v == want {
			return true
		}
	}
	return false
}
